# E3 — 변환기·저장소 장애의 격리

설계 초안일: 2026-09-11

상태: **설계 초안 — 구현과 측정 전**. 이 문서는 실험의 기술 계약 초안이다. 아래 숫자 중 "calibration에서 확정"이라고 적은 값은 로컬 calibration 결과로 바뀔 수 있으며, 확정 시 이 문서를 갱신한다. 장애 주입 기능, 격리 수단, workload와 분석 도구는 아직 구현하지 않았다.

> 이미지 변환기가 느려지거나 오류를 내거나 원본 저장소가 실패할 때, 이미 저장된 파생 이미지 요청은 얼마나 영향을 받고, 격리 수단은 그 영향을 얼마나 줄이는가?

## 읽는 순서와 현재 범위

E1은 같은 키의 동시 요청을 합쳐 중복 변환을 줄이는 방식을 채택했다. 그 결과 변환 한 건의 실패나 지연은 여러 요청이 공유한다. E1 F1은 leader 오류가 waiter에게 전파되고 후속 요청이 복구되는 것을 확인했지만, 장애가 **관련 없는 요청**에 어디까지 번지는지는 측정하지 않았다. E3는 이 질문을 다룬다.

E3는 단일 프로세스와 단일 Task 범위다. 여러 리전, CloudFront, AWS Fault Injection Service와 ECS Task 교체는 이번 실험 범위에 넣지 않는다. 장애는 [실패 시나리오](../e1-cache-stampede/README.md#실패와-취소-시나리오)와 같은 방식으로 애플리케이션의 테스트 기능으로 만든다. 그래야 로컬과 AWS에서 같은 장애를 같은 시점에 반복할 수 있다.

## 목적과 범위

운영 중 실제로 내려야 하는 결정은 다음 두 가지다.

1. 변환 경로의 장애가 캐시 적중 경로와 다른 키의 변환 경로로 번지는가? 번진다면 어떤 경로로 번지는가?
2. 번짐을 줄이기 위해 코드에 무엇을 넣을 것인가? 자동 격리 수단과 운영자의 수동 kill switch 중 무엇이 어느 상황에서 효과가 있는가?

확인하는 것:

- 장애 대상이 아닌 요청의 p50/p95/p99와 오류율이 장애 구간에서 얼마나 나빠지는가 (blast radius)
- 격리 수단이 장애 구간의 영향을 얼마나 줄이고, 정상 구간에 어떤 비용(지연·오류)을 더하는가
- 장애 시작 → 영향 시작 → 완화 → 복구의 시간 간격

확인하지 않는 것:

- 여러 Task 사이의 격리, ALB Task 교체, 리전 전환
- 실제 사용자 traffic 분포와 production SLO
- 재시도 폭주(retry storm): 부하 생성기는 재시도하지 않는다. 클라이언트 재시도가 있을 때의 증폭은 별도 질문이다.
- 원본 bypass(원본 URL로 redirect)나 placeholder 응답: 공개 원본 URL 계약이 없으므로 제외한다.

## 용어

| 용어 | 뜻 |
| --- | --- |
| hit 요청 | 파생 이미지가 이미 저장소에 있어 변환 없이 응답하는 요청 |
| miss 요청 | 파생 이미지가 없어 원본 읽기와 변환이 필요한 요청 |
| 오염 요청 (poisoned) | 장애 주입 대상 키를 요구하는 miss 요청. 이 요청이 느리거나 실패하는 것은 의도한 결과다 |
| 정상 요청 (healthy) | 장애 주입 대상이 아닌 hit 요청과 miss 요청 |
| blast radius | 장애 구간에서 정상 요청 중 timeout·오류·기준 지연 초과가 된 비율 |
| 장애 구간 | 장애 주입이 켜져 있는 시간 |
| 완화 시각 | 격리 수단이 동작하기 시작한 시각. 자동 수단은 첫 차단 시각, kill switch는 운영자가 켠 시각 |
| 복구 시각 | 장애 주입이 꺼진 뒤 정상 요청의 지표가 장애 전 수준으로 돌아온 시각 |

## 요청 처리 순서와 예상 전파 경로

현재 서비스는 요청마다 다음 순서로 처리한다. 괄호는 공유 자원이다.

```text
파생 이미지 조회 (저장소 연결)
  → [hit이면 즉시 응답]
  → 같은 키 요청 합치기 (키별 singleflight)
  → 원본 읽기 (저장소 연결, 원본 bytes를 메모리에 보유)
  → 변환 semaphore 대기 (프로세스 전체 동시 변환 상한 4)
  → libvips 변환 (CPU)
  → 파생 이미지 저장
```

결과를 보기 전의 예상 전파 경로는 다음과 같다. 어느 경로가 실제로 먼저 정상 요청을 무너뜨리는지가 이 실험의 핵심 결과다.

| 경로 | 예상 |
| --- | --- |
| 변환 semaphore | 오염 요청이 4개 slot을 오래 차지하면 정상 miss 요청이 slot을 기다린다. 가장 먼저 나타날 것으로 예상한다 |
| 원본 bytes 보유 | semaphore를 기다리는 요청은 이미 원본을 읽어 메모리에 들고 있다. 대기 요청이 늘면 memory limit에 가까워진다 |
| 요청 goroutine과 연결 | 응답하지 못한 요청이 request timeout까지 쌓인다. hit 요청은 이 경로를 공유하지만 Go HTTP 서버의 한계는 이번 부하보다 훨씬 높을 것으로 예상한다 |
| CPU | 변환 지연 자체는 CPU를 쓰지 않는다. 오류형 장애는 CPU를 거의 쓰지 않으므로 CPU 경합은 정상 miss 요청끼리만 생길 것으로 예상한다 |

예상대로라면 baseline에서 hit 요청은 코드 구조상 이미 격리되어 있고, 번짐은 주로 정상 miss 요청에 나타난다. 이 예상이 맞아도 결과로 기록한다. hit 요청이 영향을 받지 않는다는 것을 측정으로 확인하는 것도 이 실험의 목적이다.

## 만들 장애

모든 장애는 환경 변수로 켜는 실험 전용 기능이며 기본값은 꺼짐이다. 대상은 변환 spec의 특정 표식(예: 오염 전용 source hash 또는 spec 값)으로 고른다. 장애의 시작·종료 시각은 부하 생성기가 제어 endpoint로 지정한다. 장애 시각은 원자료에 기록한다.

| ID | 장애 | 만드는 방법 | 확인할 것 |
| --- | --- | --- | --- |
| T0 | 없음 | 장애 주입 꺼짐 | 격리 수단의 정상 시 비용 |
| T1 | 느린 변환 (gray) | 오염 키의 변환을 transform timeout보다 짧게 지연한 뒤 성공 | 실패 신호 없이 느려지기만 할 때 semaphore 점유가 정상 miss를 얼마나 막는가 |
| T2 | 변환 시간 초과 | 오염 키의 변환을 transform timeout보다 길게 지연 | E1 F3 후보. timeout까지 slot을 점유하고 waiter가 timeout을 공유할 때의 영향 |
| T3 | 변환 즉시 오류 | 오염 키의 변환이 지연 없이 오류 반환 | 빠른 실패는 번지지 않는가. T1/T2와 대비하는 대조군 |
| T4 | 원본 읽기 지연 | 오염 키의 원본 읽기를 지연 | 변환 semaphore 밖에서 생기는 지연은 다른 경로로 번지는가 |

지연 길이와 transform timeout은 calibration에서 확정한다. 초안 값은 아래 [고정할 실행 조건](#고정할-실행-조건)에 있다.

## 비교할 방식

| 모드 | 내용 | 정상 miss 요청에 대한 기대 |
| --- | --- | --- |
| M0 baseline | 현재 코드. request/transform timeout과 동시 변환 상한 4만 있음 | 오염 요청이 slot을 차지하는 동안 대기가 길어지고 request timeout까지 갈 수 있음 |
| M1 bounded wait + load shedding | 변환 slot 대기에 상한을 둔다. 상한을 넘으면 즉시 `503`과 `Retry-After`로 응답한다. 원본 읽기도 slot 확보 뒤로 옮겨 대기 중 원본 bytes를 들고 있지 않게 한다 | 대기 대신 빠른 실패. 오류율은 오르지만 p99와 memory는 제한됨 |
| M2 kill switch | 운영자가 제어 endpoint로 변환 경로를 끈다. miss 요청은 즉시 `503`과 `Retry-After`, hit 요청은 그대로 응답한다. 장애 시작 후 고정된 시간(초안 20초)에 켜고, 장애 종료 후 고정된 시간(초안 10초)에 끈다 | 켜진 동안 모든 miss가 실패하지만 hit은 보호됨. 탐지 지연을 고정값으로 두어 timeline을 재현 가능하게 함 |
| M3 circuit breaker (선택) | 최근 변환의 timeout/오류 비율이 기준을 넘으면 일정 시간 변환을 열어(open) 즉시 실패시키고, 반열림(half-open)에서 시험 요청으로 닫는다 | 자동 완화. M1/M2 결과가 나온 뒤 시간이 남을 때만 추가한다 |

M1과 M2는 필수, M3는 선택이다. M3를 하지 않으면 보고서에 하지 않았다고 적는다.

각 모드는 환경 변수로 선택한다. 부하 생성기와 분석기는 모드 값을 원자료에 기록하며, 모드와 장애 조합이 계획과 다르면 분석을 실패시킨다.

## Workload

한 실행(trial)은 한 모드와 한 장애 조합이다. 정상 → 장애 → 복구의 세 구간을 가진 고정 길이 timeline이며, 세 stream을 동시에 보낸다.

| stream | 요청 | 키 | 초안 rate | 목적 |
| --- | --- | --- | --- | --- |
| hit | 미리 저장한 파생 이미지 | 고정된 소수의 키 반복 | 20 req/s | hit 경로가 영향을 받는가 |
| healthy-miss | 매번 새 키의 변환 | 정상 원본 + 서로 다른 spec | 1 req/s | 변환 경로 안의 정상 요청이 영향을 받는가 |
| poisoned-miss | 오염 키의 변환 | 오염 표식 + 서로 다른 spec | 2 req/s | 장애를 실제로 만드는 요청 |

- 부하 생성기는 open loop다. 정해진 간격으로 요청을 시작하고 응답을 기다리지 않는다. 그래야 응답이 늦어질 때 요청이 실제로 쌓인다.
- 각 stream에는 동시 진행 상한을 둔다. 상한에 걸려 시작하지 못한 요청은 "생성기 포화"로 따로 세고 서버 오류와 섞지 않는다.
- 부하 생성기는 재시도하지 않는다. 클라이언트 timeout은 서버 request timeout보다 길게 둔다.
- healthy-miss는 매 요청이 실제 libvips 변환을 일으켜야 하므로 spec을 매번 바꾼다. 같은 원본의 서로 다른 크기를 사용한다.
- poisoned-miss도 spec을 매번 바꾼다. 같은 키를 반복하면 singleflight가 합쳐서 slot을 하나만 차지한다. 서로 다른 키가 slot 4개를 모두 차지하는 상황이 의도한 장애다.

Timeline 초안은 정상 30초, 장애 60초, 복구 60초의 총 150초다. Rate와 timeline 길이는 calibration에서 다음 조건으로 확정한다.

- T0에서 세 stream이 모두 오류 없이 처리되고 생성기 포화가 없다.
- M0 + T2에서 정상 miss 요청의 영향이 측정 가능하게 나타난다. 나타나지 않으면 poisoned rate를 올린다.
- 복구 구간 끝에서 정상 요청 지표가 장애 전 수준으로 돌아온다. 돌아오지 않으면 복구 구간을 늘린다.

## 고정할 실행 조건

| 항목 | 값 | 비고 |
| --- | --- | --- |
| 서비스 코드·이미지 | E1/E2와 같은 Dockerfile 계열, digest 고정 | 실행 전 commit과 digest를 원자료에 기록 |
| 원본 | E1 fixture `landscape-4928x3264.jpg` | [E1 fixture](../e1-cache-stampede/fixtures/README.md) |
| 변환 | 640 계열 cover WebP, spec의 크기만 변경 | E1과 같은 변환 경로 |
| Container limit | 1 vCPU, 2 GiB | E1/E2와 동일 |
| Coordinator | `process-singleflight` | E1 채택 결과 |
| 동시 변환 상한 | 4 | E1 calibration 값 |
| Request timeout / transform timeout | 30초 / 20초 (초안) | E1의 90/60초는 150초 timeline에 비해 너무 길다. calibration에서 확정 |
| T1 지연 / T2 지연 / T4 지연 | 15초 / 25초 / 15초 (초안) | transform timeout 기준으로 확정 |
| M1 slot 대기 상한 | 2초 (초안) | 정상 miss의 T0 p99보다 크고 request timeout보다 훨씬 짧게 |
| M2 kill switch on/off 시각 | 장애 시작 +20초 / 장애 종료 +10초 (초안) | 고정값. 자동 탐지 시간이 아니다 |
| 반복 | 모드×장애 조합별 5회 (초안) | calibration 분산이 작으면 3회로 줄일 수 있음 |
| 저장소 (로컬) | 파일 원본, 로컬 파생 디렉터리 | E1 Phase A/B와 동일 |
| 저장소 (AWS, 선택) | S3 원본과 S3 파생 | E1 AWS S4 구성 재사용 |

필수 조합은 모드 3개(M0/M1/M2) × 장애 5개(T0~T4) = 15개다. 반복 5회, timeline 150초면 본 측정은 약 3.1시간이다. M3를 추가하면 5개 조합이 늘어난다.

## 기록할 메트릭

E1의 메트릭에 다음을 더한다.

```text
media_transform_wait_seconds            변환 slot 대기 시간
media_transform_shed_total              M1에서 대기 상한 초과로 거부한 요청 수
media_kill_switch_state                 M2 kill switch 상태 (0/1)와 변경 시각
media_original_bytes_inflight           메모리에 보유 중인 원본 bytes 합계
media_fault_injections_total{fault=...} 장애 주입이 적용된 요청 수
```

요청별 결과에는 E1 항목에 stream 종류, 오염 여부, 장애 ID, 모드, 요청 시작 시각의 구간(정상/장애/복구), 응답 상태, `Retry-After` 유무를 더한다. 자원 sampling은 E1과 같은 cgroup 기반이다.

## 결과에서 보여줄 것

1. **Timeline chart**: 초 단위로 hit과 healthy-miss stream의 p99와 오류율을 그리고 장애 구간과 완화 시각을 음영·선으로 표시한다. M0/M1/M2를 같은 축에 놓는다. 장애 종류별로 한 장씩 만든다.
2. **Blast radius 표**: 모드×장애별로 정상 요청 중 실패·기준 지연 초과 비율, 정상 miss p99, hit p99, peak memory를 적는다.
3. **정상 시 비용 표**: T0에서 M1/M2가 M0 대비 더한 지연과 오류.
4. **시간 간격 표**: 장애 시작 → 정상 요청 영향 시작 → 완화 → 복구.

그래프는 원자료에서 다시 만들 수 있어야 한다. 축 이름과 단위를 적고 색만으로 구분하지 않는다.

## 결과를 보고 내릴 결정

- 정상 miss 요청이 M0에서 request timeout에 도달한다면 M1의 bounded wait를 기본값으로 채택한다. 도달하지 않고 지연만 늘어난다면 대기 상한 값을 다시 논의한다.
- hit 요청이 M0에서도 영향을 받지 않는다면, 코드 구조상 격리가 이미 있다고 결론을 내리고 kill switch의 가치는 miss 경로 보호로 한정한다.
- M2 kill switch는 효과와 무관하게 유지한다. 자동 수단이 판단하지 못하는 장애에 대한 운영자의 최후 수단이기 때문이다. 다만 켜고 끄는 시간과 영향 범위를 측정값으로 기록한다.
- 되돌릴 조건: M1의 shedding이 정상 시 오류를 만들거나, 대기 상한이 실제 변환 시간 분포보다 짧아 정상 요청을 거부하면 상한을 올리거나 M0로 되돌린다.

## 결과 파일

```text
experiments/e3-failure-isolation/
  README.md
  results/<run-id>/
    run.json                   실행 환경, 모드, 장애 조건, 시각
    trials.csv                 모드×장애×반복의 유효 여부와 요약
    requests.csv               요청별 결과 (stream, 구간, 상태, 지연)
    timeline.csv               초 단위 stream별 집계
    resources.csv              시간대별 CPU·메모리
    metrics.prom               메트릭 원본
    logs.jsonl                 장애 주입·kill switch 이벤트를 포함한 로그
    analysis/
      analysis.json            raw 교차 검증과 집계 조건
      blast-radius.csv         모드×장애 기반 표
      timeline-<fault>.svg     장애별 timeline chart
      normal-cost.csv          정상 시 비용 표

cmd/e3-runner/                 실행과 분석 CLI (예정)
internal/e3runner/             workload, raw writer, 검증과 chart 코드 (예정)
internal/media/                장애 주입, bounded wait, kill switch (예정)
```

## 재현 경로 (예정)

구현 후 이 절에 Docker build, 로컬 실행, calibration, 본 측정과 분석 명령을 적는다. 명령은 공개 저장소 안의 것만 사용한다.

AWS 단계는 선택이다. 진행하면 [E1 AWS S4 Terraform](../../deploy/e1-aws-s4/README.md)을 Task 1개 구성으로 재사용하고, S3 원본·파생 저장소에서 같은 workload를 실행한다. ALB health check가 장애 구간에서 어떻게 반응하는지와 S3 지연이 로컬 파일과 어떻게 다른지를 추가로 본다. AWS 단계를 생략하면 보고서에 생략했다고 적고 로컬 결과의 한계로 기록한다.

## 완료 조건

- T0~T4 × M0/M1/M2 조합을 각각 유효하게 계획한 횟수만큼 측정했다.
- 각 요청이 어느 stream, 어느 구간, 어느 모드·장애에 속하는지 원자료에 있고, 분석 명령이 요청·timeline·메트릭·이벤트 로그의 정합성을 자동 검사한다.
- 장애별 timeline chart, blast radius 표, 정상 시 비용 표, 시간 간격 표를 원자료에서 다시 만들 수 있다.
- 보고서에 예상 전파 경로와 실제 결과의 차이, 채택한 수단, 되돌릴 조건, 확인하지 못한 것을 적었다.
- AWS 단계를 실행했다면 자원 제거와 잔여 0개를 확인했고, 실행하지 않았다면 그 사실을 적었다.
