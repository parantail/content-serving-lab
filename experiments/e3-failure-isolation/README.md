# E3 — 변환기·저장소 장애의 격리

측정일: 2026-09-11 · 문서 갱신일: 2026-09-13

상태: **로컬·AWS 본 측정 완료**. [결과 보고서](../../reports/e3-failure-isolation/README.md)는 hit 경로가 모든 장애에서 격리됨을 확인했고, 정상 miss p99 17.3초를 slot 대기 상한과 503 거부로 약 2초로 줄인 결과에 따라 M1을 채택했다. M1의 원본 읽기 순서(T4 상충)와 kill switch 사용 조건은 재검토 대상이다. 로컬 `retained-e8c21674a86f…` 75 trial과 AWS `aws-measure-3040e3db9be0` 18 trial이 모두 유효했다. 이 문서는 실험의 기술 계약이다. 서비스 쪽의 [제어 계약](#구현한-제어-계약)(변환 gate, kill switch, 장애 주입, 제어 endpoint), 부하 생성기·분석기·차트([로컬 재현](#로컬-재현))를 구현하고 test로 고정했다. 실행 조건은 로컬 calibration(1 vCPU/2 GiB, 세 모드 × 다섯 장애 각 1회)에서 확정했으며 아래 표의 값이 `run`의 기본값이다.

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
| 대응 기준 시각 | `intervals.csv`의 mitigation: M1은 처음 shed된 요청의 시작 시각, M2는 실제 kill switch on 시각. M1의 응답 시각과는 다름 |
| 장애 종료 후 마지막 영향 요청 시작 | 장애 종료 후 시작한 정상 요청 중 마지막으로 영향을 받은 요청의 시작 offset. 추가 영향이 없으면 0이며 진행 중 요청의 완료 시각과는 다름 |

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
| T2 | 변환 시간 초과 | 오염 키의 변환을 transform timeout이 끝날 때까지 정지시킴 (지연값 없음) | E1 F3 후보. timeout까지 slot을 점유하고 waiter가 timeout을 공유할 때의 영향 |
| T3 | 변환 즉시 오류 | 오염 키의 변환이 지연 없이 오류 반환 | 빠른 실패는 번지지 않는가. T1/T2와 대비하는 대조군 |
| T4 | 원본 읽기 지연 | 오염 키의 원본 읽기를 지연 | 변환 semaphore 밖에서 생기는 지연은 다른 경로로 번지는가 |

지연 길이와 transform timeout은 아래 [고정할 실행 조건](#고정할-실행-조건)에 있다. Calibration에서 다섯 장애 모두 오염 요청에만 주입되고 정상 원본 요청에는 주입되지 않는 것을 카운터로 확인했다.

## 비교할 방식

| 모드 | 내용 | 정상 miss 요청에 대한 기대 |
| --- | --- | --- |
| M0 baseline | 현재 코드. request/transform timeout과 동시 변환 상한 4만 있음 | 오염 요청이 slot을 차지하는 동안 대기가 길어지고 request timeout까지 갈 수 있음 |
| M1 bounded wait + load shedding | 변환 slot 대기에 상한을 둔다. 상한을 넘으면 즉시 `503`과 `Retry-After`로 응답한다. 원본 읽기도 slot 확보 뒤로 옮겨 대기 중 원본 bytes를 들고 있지 않게 한다 | 대기 대신 빠른 실패. 오류율은 오르지만 p99와 memory는 제한됨 |
| M2 kill switch | 운영자가 제어 endpoint로 변환 경로를 끈다. miss 요청은 즉시 `503`과 `Retry-After`, hit 요청은 그대로 응답한다. 장애 시작 후 고정된 시간(20초)에 켜고, 장애 종료 후 고정된 시간(10초)에 끈다 | 켜진 동안 모든 miss가 실패하지만 hit은 보호됨. 탐지 지연을 고정값으로 두어 timeline을 재현 가능하게 함 |
| M3 circuit breaker (범위 제외) | 최근 변환의 timeout/오류 비율이 기준을 넘으면 일정 시간 변환을 열어(open) 즉시 실패시키고, 반열림(half-open)에서 시험 요청으로 닫는다 | 자동 완화 후보였으나 구현·측정하지 않았다 |

실제 비교는 M0·M1·M2로 완료했다. M3는 구현·측정하지 않았다.

M1은 원본 읽기를 slot 확보 뒤로 옮기므로, T4(원본 읽기 지연)에서는 오염 요청이 원본을 기다리는 동안에도 slot을 점유한다. M0에서는 slot 밖에서 기다린다. 이 차이는 M1 설계의 결과이며 T4 결과를 해석할 때 함께 기록한다.

각 모드는 환경 변수로 선택한다. 부하 생성기와 분석기는 모드 값을 원자료에 기록하며, 모드와 장애 조합이 계획과 다르면 분석을 실패시킨다.

## Workload

한 실행(trial)은 한 모드와 한 장애 조합이다. 정상 → 장애 → 복구의 세 구간을 가진 고정 길이 timeline이며, 세 stream을 동시에 보낸다.

| stream | 요청 | 키 | 확정 rate | 목적 |
| --- | --- | --- | --- | --- |
| hit | 미리 저장한 파생 이미지 | 고정된 4개 키 반복 (`width=640..643,height=480`) | 20 req/s | hit 경로가 영향을 받는가 |
| healthy-miss | 매번 새 키의 변환 | 정상 원본 + 서로 다른 spec | 0.5 req/s | 변환 경로 안의 정상 요청이 영향을 받는가 |
| poisoned-miss | 오염 키의 변환 | 오염 표식 + 서로 다른 spec | 1 req/s | 장애를 실제로 만드는 요청 |

Miss rate는 calibration에서 초안(1/2 req/s)의 절반으로 확정했다. 초안 rate에서는 T0 baseline의 CPU 시간이 150초 중 144초로 포화되어 정상 miss p50이 약 1초, slot 대기가 최대 6건이었다. 절반 rate에서는 CPU 약 46%, slot 대기 0, 정상 miss p50 약 0.55초였다. 변환 1건이 1 vCPU에서 약 0.3~0.5초를 쓰므로 miss 합계 1.5 req/s가 baseline을 큐잉 없이 유지하는 상한 근처다.

- 부하 생성기는 open loop다. 정해진 간격으로 요청을 시작하고 응답을 기다리지 않는다. 그래야 응답이 늦어질 때 요청이 실제로 쌓인다.
- 각 stream에는 동시 진행 상한을 둔다. 상한에 걸려 시작하지 못한 요청은 "생성기 포화"로 따로 세고 서버 오류와 섞지 않는다.
- 부하 생성기는 재시도하지 않는다. 클라이언트 timeout은 서버 request timeout보다 길게 둔다.
- healthy-miss는 매 요청이 실제 libvips 변환을 일으켜야 하므로 spec을 매번 바꾼다. 같은 원본의 서로 다른 크기를 사용한다.
- poisoned-miss도 spec을 매번 바꾼다. 같은 키를 반복하면 singleflight가 합쳐서 slot을 하나만 차지한다. 서로 다른 키가 slot 4개를 모두 차지하는 상황이 의도한 장애다.

Timeline은 정상 30초, 장애 60초, 복구 60초의 총 150초다. Calibration에서 다음 세 조건을 확인했다.

- T0에서 세 모드 모두 세 stream이 오류 없이 처리되고 생성기 포화가 없었다.
- M0 + T2에서 정상 miss 요청의 93%가 기준 지연을 넘었고(p99 약 17.3초), M1은 약 83%를 slot 대기 상한 2초 뒤 거부했으며, M2는 kill switch 전에는 M0처럼 대기하고 켜진 뒤에는 즉시 거부했다.
- M0 + T2의 정상 miss 지연은 장애 종료 약 15초 뒤에 장애 전 수준으로 돌아왔다. 60초 복구 구간이 충분했다.

## 고정할 실행 조건

| 항목 | 값 | 비고 |
| --- | --- | --- |
| 서비스 코드·이미지 | E1/E2와 같은 Dockerfile 계열, digest 고정 | 실행 전 commit과 digest를 원자료에 기록 |
| 원본 | E1 fixture `landscape-4928x3264.jpg` | [E1 fixture](../e1-cache-stampede/fixtures/README.md) |
| 변환 | 640 계열 cover WebP, spec의 크기만 변경 | E1과 같은 변환 경로 |
| Container limit | 1 vCPU, 2 GiB | E1/E2와 동일 |
| Coordinator | `process-singleflight` | E1 채택 결과 |
| 동시 변환 상한 | 4 | E1 calibration 값 |
| Request timeout / transform timeout | 30초 / 20초 | E1의 90/60초는 150초 timeline에 비해 너무 길다. Transform timeout이 slot 대기·원본 읽기·변환을 함께 묶으므로 client timeout보다 짧게 두어 서버가 요청을 끝낸다. Calibration에서 client timeout 0건 확인 |
| T1 지연 / T4 지연 | 15초 / 15초 | transform timeout보다 짧아 오염 요청이 결국 성공한다 |
| T2 | 지연값 없음. transform timeout까지 정지 | timeout 값이 곧 T2의 점유 시간 |
| M1 slot 대기 상한 | 2초 | 정상 miss의 T0 p99(약 0.6초)의 3배 이상이고 request timeout보다 훨씬 짧다. Calibration T0에서 M1의 거부 0건 |
| M2 kill switch on/off 시각 | 장애 시작 +20초 / 장애 종료 +10초 | 고정값. 자동 탐지 시간이 아니다 |
| 반복 | 모드×장애 조합별 5회 (로컬), 2회 (AWS 본 측정; 실행 스크립트 기본값은 3회) | Calibration 1회에서 M0/M1/M2 차이가 분명했으므로 로컬은 5회를 유지한다 |
| 저장소 (로컬) | 파일 원본, 로컬 파생 디렉터리 | E1 Phase A/B와 동일 |
| 저장소 (AWS) | 같은 S3 버킷의 `originals/`와 `derivatives/` | [E3 one-shot Fargate](../../deploy/e3-failure-isolation/README.md) |

필수 조합은 모드 3개(M0/M1/M2) × 장애 5개(T0~T4) = 15개다. 반복 5회, timeline 150초면 본 측정은 약 3.3시간이다. AWS 본 측정은 T0·T2·T4 × 세 모드 × 2회로 18 trial을 완료했다. 초기 계획과 실행 스크립트의 기본값은 3회/27 trial이지만, 실제 실행은 남은 deadline에 맞춰 `--repetitions 2`를 지정했다.

## 구현한 제어 계약

서비스 프로세스는 다음 환경 변수로 모드와 실험 기능을 고른다. 기본값은 모두 실험 기능이 꺼진 상태다.

| 환경 변수 | 값 | 뜻 |
| --- | --- | --- |
| `ISOLATION_MODE` | `baseline` (기본) / `bounded-wait` / `kill-switch` | 비교 모드 M0 / M1 / M2 |
| `TRANSFORM_CONCURRENCY` | 기본 4 | 프로세스 전체 동시 변환 상한. 모든 모드에서 processor의 변환 gate가 소유한다 |
| `E3_SLOT_WAIT_LIMIT` | 기본 `2s` | `bounded-wait`에서 slot 대기 상한. 다른 모드에서는 무시 |
| `E3_POISONED_SOURCE_HASH` | SHA-256 hex | 장애 주입 대상 source hash. 로컬 저장소에서는 같은 fixture의 별칭으로 등록한다. S3에서는 이 키로 원본을 미리 올린다. 비어 있으면 장애 주입 비활성 |
| `E3_CONTROL_MODE` | `true` | 아래 제어 endpoint를 연다. 기본은 닫힘 |

제어 endpoint는 `E3_CONTROL_MODE=true`일 때만 존재한다.

| 요청 | 본문 | 응답 |
| --- | --- | --- |
| `GET /internal/e3/state` | 없음 | 모드, 장애 상태, kill switch 상태와 전환 이력, gate 상태, processor 상태, 메트릭 snapshot |
| `POST /internal/e3/fault` | `{"fault":"none\|slow-transform\|transform-timeout\|transform-error\|slow-original","delay":"15s"}` | 200과 상태. 잘못된 종류·지연은 400, 장애 주입 비활성이면 409 |
| `POST /internal/e3/kill-switch` | `{"enabled":true}` | 200과 상태. `kill-switch` 모드가 아니면 409 |

장애는 오염 source hash의 요청에만 적용된다. `slow-transform`과 `slow-original`은 양의 지연이 필요하고, `transform-error`는 선택적 지연 뒤 오류를 반환하며, `transform-timeout`은 transform timeout이 끝날 때까지 정지한다. 장애 훅은 변환 slot 안에서 실행되므로 T1~T3는 slot을 점유한다.

격리 수단이 요청을 일찍 끝내면 `503`, `Retry-After: 1`, `Cache-Control: no-store`와 `X-Media-Isolation: shed` 또는 `kill-switch` 헤더로 응답한다. `Retry-After` 값은 고정 실험 상수다. 그 밖의 오류 응답은 E1과 같다.

## 기록할 메트릭

E1의 메트릭에 다음을 더한다.

```text
media_transform_wait_seconds_sum / _count   변환 slot 대기 시간 합과 횟수 (거부·timeout 포함)
media_transform_shed_total                  대기 상한 초과로 거부한 요청 수
media_kill_switch_state                     kill switch 상태 (0/1)
media_kill_switch_rejected_total            kill switch가 거부한 miss 요청 수
media_original_bytes_inflight               메모리에 보유 중인 원본 bytes 합계
media_fault_injections_total{fault=...}     장애 주입이 적용된 요청 수 (종류별)
```

`media_transform_inflight`는 slot을 확보하고 변환 중인 요청 수이며, slot을 기다리는 요청은 제어 endpoint의 gate `waiting`에 따로 보인다. E1에서는 transformer 내부 semaphore 대기가 inflight에 포함됐다.

요청별 결과에는 E1 항목에 stream 종류, 오염 여부, 장애 ID, 모드, 요청 시작 시각의 구간(정상/장애/복구), 응답 상태, `X-Media-Isolation`과 `Retry-After` 유무를 더한다. 자원 sampling은 E1과 같은 cgroup 기반이다.

## 결과에서 보여줄 것

1. **Timeline chart**: 요청 시작 시각 기준 5초 bucket으로 hit과 healthy-miss stream의 p99와 영향 비율(실패 또는 기준 지연 초과)을 그리고 장애 구간과 kill switch 시각을 음영·선으로 표시한다. M0/M1/M2를 같은 축에 놓는다. 장애 종류별로 한 장씩 만든다.
2. **Blast radius 표**: 모드×장애×stream×구간별로 정상 요청 중 실패·기준 지연 초과 비율(affected rate), p50/p95/p99와 최대 지연, 거부·kill switch 비율을 적는다. 자원 표에는 peak cgroup memory, 보유 원본 bytes 최대, slot 대기 최대와 오염 stream의 결과를 둔다.
3. **정상 시 비용 표**: T0에서 M1/M2가 M0 대비 더한 지연과 오류.
4. **시간 간격 표**: 첫 영향 요청 시작, 대응 기준 시각(M1은 첫 shed 요청 시작, M2는 실제 kill switch on), 장애 종료 후 마지막 영향 요청 시작을 표시한다. 요청 종료·실제 차단 시각과 구분한다.

기준 지연은 stream별로 모든 유효 trial의 정상 구간 p99 중 최댓값의 3배다. hit stream의 정상 구간 p99는 동시에 실행 중인 변환의 CPU 경합에 따라 1ms에서 20ms대까지 흔들리므로 중앙값 대신 최댓값을 쓴다. 비율과 percentile은 trial마다 계산한 뒤 평균해 trial 경계를 보존한다. 그래프는 원자료에서 다시 만들 수 있어야 한다. 축 이름과 단위를 적고 색만으로 구분하지 않는다.

## 사전 선택 기준과 실제 결정

아래는 측정 전 선택 기준이다. 실제로는 request timeout에 도달하지 않았으며, [보고서](../../reports/e3-failure-isolation/README.md#결과를-보고-무엇을-선택했나)에 기준과 다른 선택의 이유와 적용 조건을 기록했다.

- 정상 miss 요청이 M0에서 request timeout에 도달한다면 M1의 bounded wait를 기본값으로 채택한다. 도달하지 않고 지연만 늘어난다면 대기 상한 값을 다시 논의한다.
- hit 요청이 M0에서도 영향을 받지 않는다면, 코드 구조상 격리가 이미 있다고 결론을 내리고 kill switch의 가치는 miss 경로 보호로 한정한다.
- M2 kill switch는 효과와 무관하게 유지한다. 자동 수단이 판단하지 못하는 장애에 대한 운영자의 최후 수단이기 때문이다. 다만 켜고 끄는 시간과 영향 범위를 측정값으로 기록한다.
- 되돌릴 조건: M1의 shedding이 정상 시 오류를 만들거나, 대기 상한이 실제 변환 시간 분포보다 짧아 정상 요청을 거부하면 상한을 올리거나 M0로 되돌린다.

## 결과 파일

```text
experiments/e3-failure-isolation/
  README.md
  results/<run-id>/
    run.json                   실행 환경, 모드, 장애 조건, 초안/확정 값, 실행 명령
    trials.csv                 모드×장애×반복의 유효 여부, 이벤트 시각, 카운터와 구간별 요약
    requests.csv.gz            요청별 결과 (stream, 구간, 상태, 지연, 격리 헤더). 사전 예열 요청도 포함. gzip
    state.csv                  1초 간격의 서버 상태 (inflight, slot 대기, 보유 원본 bytes, kill switch, 누적 카운터)
    resources.csv.gz           100ms 간격의 cgroup CPU·메모리. gzip
    metrics.prom               trial별 메트릭 증분
    logs.jsonl                 장애 주입·kill switch·구간 경계 이벤트
    analysis/
      analysis.json            raw 교차 검증, 무효 trial과 이유, 기준 지연
      blast-radius.csv         모드×장애×stream×구간 표
      resources.csv            모드×장애 자원·오염 stream 요약
      normal-cost.csv          정상 시 비용 표
      intervals.csv            시간 간격 표
      timeline-summary.csv     5초 bucket 집계 (chart의 입력)
      timeline-<fault>.svg     장애별 timeline chart

cmd/e3-runner/                 실행과 분석 CLI
internal/e3runner/             workload, raw writer, 검증, 집계와 chart 코드
internal/media/                변환 gate, bounded wait, kill switch, 장애 주입
internal/e3control/            제어 endpoint의 상태·명령
```

Runner는 trial마다 새 processor·저장소·메트릭으로 서비스를 같은 프로세스 안에 띄우고 loopback HTTP로 세 stream을 보낸다. 장애와 kill switch는 같은 컨트롤러를 직접 호출하며 시각을 `logs.jsonl`에 남긴다. 분석은 `requests.csv.gz`의 요청 수·격리 결과·hit/miss 분류를 `trials.csv`와 `metrics.prom`의 카운터, `logs.jsonl`의 이벤트 시각과 대조하고 불일치한 trial을 무효로 표시한다.

## 로컬 재현

Docker와 Git LFS가 필요하다. `run`은 완료 후 원자료를 교차 검증하고 표와 차트를 만든다.

```bash
git lfs pull
commit="$(git rev-parse HEAD)"
image="content-serving-e3:${commit}"
docker build --target experiment-e3 --build-arg "GIT_COMMIT=${commit}" -t "${image}" .
image_id="$(docker image inspect --format '{{.Id}}' "${image}")"

mkdir -p experiments/e3-failure-isolation/results
docker run --rm --cpus=1 --memory=2g \
  --mount "type=bind,source=${PWD}/experiments/e3-failure-isolation/results,target=/results" \
  "${image}" run \
  --run-id "retained-${commit}" \
  --container-image "${image}#${image_id}"
```

`run`의 기본 flag가 확정 실행 조건이다. `--calibration`은 결과를 calibration으로 표시하고, `--modes`, `--faults`, `--repetitions`로 부분 matrix를 실행할 수 있다. 기존 원자료에서 검증과 차트만 다시 만들 때는 다음을 사용한다.

```bash
docker run --rm \
  --mount "type=bind,source=${PWD}/experiments/e3-failure-isolation/results,target=/results" \
  "${image}" analyze --run-dir "/results/retained-${commit}"
```

AWS 단계는 [E3 Fargate 배포](../../deploy/e3-failure-isolation/README.md)를 사용한다. 같은 image의 `run --storage s3`가 Task 1개 안에서 서비스와 부하 생성기를 함께 실행하고 원본·파생 저장소만 S3를 쓴다. 하나의 버킷이 여러 run의 파생 이미지를 보관하므로 S3 모드에서는 fixture를 run ID로 파생한 별칭 hash(`run.json`의 `source_hash`)로 올려 run 사이에 파생 키가 겹치지 않게 한다. `fixture_sha256`은 실제 fixture hash다. ALB는 없으므로 health check 반응은 보지 않는다. Fargate의 vCPU는 로컬 calibration host보다 느려 AWS 단계의 stream rate는 `run.ps1`의 `-RunnerArgs` 기본값(hit 5, healthy-miss 0.2, poisoned-miss 0.4 req/s)으로 낮춘다.

## 완료 조건

- T0~T4 × M0/M1/M2 조합을 각각 유효하게 계획한 횟수만큼 측정했다.
- 각 요청이 어느 stream, 어느 구간, 어느 모드·장애에 속하는지 원자료에 있고, 분석 명령이 요청·timeline·메트릭·이벤트 로그의 정합성을 자동 검사한다.
- 장애별 timeline chart, blast radius 표, 정상 시 비용 표, 시간 간격 표를 원자료에서 다시 만들 수 있다.
- 보고서에 예상 전파 경로와 실제 결과의 차이, 채택한 수단, 되돌릴 조건, 확인하지 못한 것을 적었다.
- AWS 단계를 실행했다면 자원 제거와 잔여 0개를 확인했고, 실행하지 않았다면 그 사실을 적었다.

## Calibration에서 확인한 것

Calibration 실행은 대표 결과에 포함하지 않으며 실행 조건을 정하는 데만 사용했다. 1회씩이지만 다음 경향이 분명했다.

- 세 모드 모두 다섯 장애에서 hit stream의 장애 구간 오류는 0이었고 p99는 1ms 안팎이었다. hit p99는 정상·복구 구간에서 20ms대로 튀는데, 이는 동시에 실행 중인 변환의 CPU 경합 때문이며 장애 구간(변환 정지 = CPU 유휴)에서는 오히려 낮아진다.
- M0 + T1/T2: 정상 miss의 90% 이상이 기준 지연을 넘었고 p99는 약 17초였다. 오류는 없었다. slot 대기 최대 25건, 보유 원본 bytes 최대 약 113 MiB, peak cgroup memory 약 0.9~1.1 GiB.
- M1 + T1/T2: 정상 miss의 80% 이상이 slot 대기 상한 2초 뒤 거부됐다. slot 대기 최대 3~4건, 보유 원본 약 16 MiB.
- M0 + T4: 정상 miss에 영향이 없었다. 원본 읽기 지연은 slot 밖에서 일어나기 때문이다. 반면 M1 + T4에서는 slot을 먼저 잡고 읽기를 기다리므로 정상 miss의 87%가 거부됐다. M1의 설계 상충이 그대로 나타났다.
- T3(즉시 오류)는 M0/M1에서 정상 miss에 영향을 주지 않았다. M2는 kill switch가 켜진 동안 정상 miss를 거부했다.
- M2는 kill switch를 켜기 전 20초 동안 M0와 같았고, 켜진 뒤에는 장애 종류와 무관하게 모든 miss를 거부했다. 번지지 않는 장애(T3, T4)에서도 정상 miss를 거부했다.
