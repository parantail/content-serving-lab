# E1 — 캐시 폭주와 동일 요청 합치기

상태: **Phase A와 로컬 Phase B 측정 완료 — AWS S4의 S3 store·Task 계측 구현 완료**

대표 결과와 결정은 [Phase A: 동시 cold miss 100개를 이미지 변환 한 번으로 합칠 수 있는가?](../../reports/e1-cache-stampede/README.md)와 [Phase B: 서로 다른 변환 요청과 여러 프로세스에서는 어디까지 합칠 수 있는가?](../../reports/e1-cache-stampede/PHASE-B.md)에서 확인할 수 있습니다.

> 아직 변환된 이미지가 없을 때 같은 요청이 한꺼번에 들어오면, 실제 변환 횟수를 얼마나 줄이면서 응답 지연과 오류를 억제할 수 있을까?

아래 조건과 판단 기준을 기준으로 실험합니다. 조건을 수정한 실행은 변경 내용을 기록하고 이전 실행과 나누어 집계합니다.

## 목적과 범위

E1은 첫 변환이 진행되는 동안 같은 요청이 여러 개 도착했을 때 생기는 중복 작업을 다룹니다. 요청마다 원본을 읽고 같은 이미지를 반복해서 변환하면 CPU와 메모리 사용량이 커지고 다른 이미지 요청까지 느려질 수 있습니다. 이미지 한 장의 변환 속도와 메모리 사용량은 E2에서 libvips와 ImageMagick을 같은 조건으로 비교합니다.

이번 실험에서는 같은 키의 요청을 한 프로세스 안에서 하나로 합쳤을 때 다음 값이 어떻게 달라지는지 봅니다.

- 실제 이미지 변환 횟수
- 원본을 읽은 횟수와 파생 이미지를 저장하려 한 횟수
- p50, p95와 p99 응답 시간
- CPU 사용 시간과 최대 메모리
- 시간 초과와 오류

Phase A에서는 한 프로세스에 같은 이미지 요청이 몰리는 상황과 첫 변환이 실패하는 상황을 확인했습니다. Phase B에서는 인기 이미지가 변환되는 동안 다른 이미지 요청도 진행되는지, 최초 요청이 취소돼도 나머지 요청은 완료되는지, 프로그램을 여러 개 실행하면 중복 변환이 몇 번 생기는지를 확인했습니다. 실제 S3와 ECS, 여러 프로세스를 하나로 묶는 분산 조정은 아직 측정하지 않았으므로 로컬 결과만으로 결론 내리지 않습니다.

## 용어

- **파생 이미지(Derivative)**: 원본을 특정 크기와 포맷으로 변환해 만든 이미지입니다. 원본이 있으면 다시 만들 수 있습니다.
- **변환 옵션 정규화**: `width=640&format=webp`와 순서만 다른 요청처럼 의미가 같은 옵션을 하나의 표현으로 맞추는 일입니다. 정규화된 값이 같으면 같은 파생 이미지 키를 사용합니다. 기술 문서에서는 이를 canonicalization이라고도 합니다.
- **동일 요청 합치기**: 같은 파생 이미지가 필요한 요청 여러 개 중 하나만 실제 변환을 수행하고 나머지는 그 결과를 기다리게 하는 방식입니다.
- **leader**: 같은 키로 들어온 요청 중 실제 변환을 수행하는 요청입니다.
- **waiter**: 같은 키의 leader가 만드는 결과를 기다리는 요청입니다.
- **Cold 상태**: 원본은 있지만 요청한 파생 이미지와 CDN 캐시는 아직 없는 상태입니다.

## 요청 처리 순서

```text
GET /i/{source_hash}/{transform_spec}.{format}
  → 변환 옵션 검사 및 정규화
  → 파생 이미지 키 계산
  → 저장된 파생 이미지 조회
  → 같은 키로 동시에 들어온 요청 합치기
  → 원본 읽기와 이미지 변환
  → 완성된 파생 이미지 저장
  → 이미지와 캐시 정보를 응답
```

첫 로컬 실험은 CDN 없이 Media Service에 직접 요청합니다. 업로드 API, CDN 적중 요청의 속도, 멀티 리전 전환은 이번 실험 범위에 넣지 않습니다.

## 예상

1. 요청을 합치지 않으면 동시 요청이 늘어날수록 실제 변환과 원본 읽기 횟수도 요청 수에 가깝게 늘어날 것입니다.
2. 한 프로세스 안에서 요청을 합치면 같은 키의 성공적인 변환은 한 번만 실행될 것입니다.
3. 같은 키의 요청을 합치는 동안에도 다른 키의 요청은 따로 진행할 수 있어야 합니다.
4. ECS Task가 여러 개면 각 프로세스에서 한 번씩 변환할 수 있으므로 전체 변환은 한 번으로 줄지 않을 수 있습니다.
5. leader가 실패하면 waiter도 같은 오류를 받고 동시에 재시도해 두 번째 폭주가 생길 수 있습니다.

## 비교할 방식

요청을 합치는 범위와 S3 저장 방식은 별개의 선택입니다.

| 실행 구성 | 요청 합치기 | S3 저장 | 예상 변환 횟수 | 예상 저장 결과 |
| --- | --- | --- | ---: | --- |
| 단일 프로세스, 비교 기준 | 없음 | 각 요청이 저장 시도 | 요청 수에 가까움 | 여러 번 저장될 수 있음 |
| 단일 프로세스 | 프로세스 내부 | leader가 한 번 저장 | 1회 | 1회 저장 |
| 여러 프로세스 | 각 프로세스 내부 | S3 conditional write | 요청을 받은 프로세스당 최대 1회 | 한 프로세스만 새 파일 생성 |
| 여러 프로세스 | 프로세스 사이에서도 조정 | S3 conditional write를 안전장치로 유지 | 전체 1회 목표 | 한 프로세스만 새 파일 생성 |

S3 conditional write는 `If-None-Match: *` 조건을 사용해 해당 키가 없을 때만 객체를 만드는 방식입니다. 먼저 저장한 프로세스만 성공하고 나머지는 이미 객체가 있다는 응답을 받습니다. 나머지 프로세스가 수행한 이미지 변환까지 없애 주지는 않습니다.

### AWS S4 S3 store 계약

AWS S4에서 사용할 원본과 파생 이미지 store는 AWS SDK for Go v2의 `GetObject`와 `PutObject`를 사용합니다. 객체 key는 bucket 안에서 다음처럼 고정합니다.

```text
Original bucket:   originals/{source_hash}
Derivative bucket: derivatives/{derivative_key}.webp
```

파생 이미지 저장 request에는 `If-None-Match: *`와 `Content-Type: image/webp`를 직접 설정합니다. `200 OK`는 `created`, `412 Precondition Failed`는 다른 Task가 먼저 저장한 정상 경쟁 결과인 `existing`으로 처리합니다. `409 ConditionalRequestConflict`는 store 계층에서 최대 두 번 더 호출한 뒤에도 계속되면 별도 오류로 반환합니다. `existing`을 받은 뒤 winner object를 다시 읽는 처리는 기존 media processor 계약을 그대로 사용합니다.

파생 이미지 조회는 `404 Not Found` 또는 `NoSuchKey`만 miss로 처리하고 `403 Access Denied`를 비롯한 다른 응답은 저장소 오류로 반환합니다. S3는 호출자에게 `s3:ListBucket` 권한이 없으면 존재하지 않는 key의 `GetObject`에도 403을 반환할 수 있으므로, AWS IAM에서는 파생 이미지만 담는 전용 Derivative bucket 하나에 `s3:ListBucket`을 허용합니다. 이 bucket 단위 권한은 cold miss와 권한 오류를 구분하기 위한 절충이며 Original/Result bucket이나 account의 다른 bucket에는 적용하지 않습니다.

현재 완료된 범위는 store 구현과 SDK request 단위 test입니다. 실행 중인 Media Service가 환경 설정에 따라 S3 store를 선택하는 wiring과 IAM/Terraform은 후속 AWS S4 단계에서 추가합니다.

### AWS S4 Task 식별과 trial 계측 계약

AWS S4 실험 모드는 기본적으로 꺼져 있습니다. `E1_EXPERIMENT_MODE=true`일 때만 시작 과정에서 `ECS_CONTAINER_METADATA_URI_V4`의 `/task`를 한 번 읽고 내부 제어 endpoint를 등록합니다. `E1_CONTAINER_NAME`으로 지정한 container를 찾으며 기본 이름은 `media-service`입니다. 필요한 metadata를 읽지 못하거나 container image digest가 `sha256:` digest 형식이 아니면 실험 모드로 기동하지 않습니다.

Task identity에는 account ID와 전체 ARN을 넣지 않고 다음 값만 유지합니다.

- Task ID, Task definition family와 revision
- Availability Zone과 launch type
- Task CPU·memory limit
- Media Service container image digest

실험 모드의 이미지 응답은 `X-E1-Task-ID`와 요청에서 받은 `X-E1-Trial-ID`를 반환합니다. 이미지 요청에는 준비된 trial ID가 반드시 있어야 하며, 다른 trial이 준비되어 있거나 진행 중인 요청·변환·coordinator 항목이 남아 있으면 trial 경계를 넘기지 않습니다.

| Method와 path | 역할 |
| --- | --- |
| `POST /internal/e1/trials/{trial_id}/prepare` | Task를 trial에 등록하고 counter baseline과 resource sampling을 시작 |
| `POST /internal/e1/trials/{trial_id}/finish` | 요청과 공유 작업이 모두 끝난 뒤 Task report와 resource sample을 반환 |

같은 Task에 같은 trial의 prepare/finish를 다시 호출해도 같은 상태나 완료 report를 반환합니다. 제어 응답은 `Cache-Control: no-store`를 사용하며 report schema는 `e1-aws-s4-task-v1`입니다. Report에는 Task별 이미지 요청 수, 파생/원본 S3 GET 결과와 byte, 변환 시도·성공·실패·시간·최대 동시 실행 수, coalesced 요청 수, S3 publish의 created/existing/conflict/error와 시도 byte, 첫/마지막 요청 시각, 종료 시점의 진행 중 요청·변환·coordinator 상태가 들어갑니다.

Resource sample은 ECS metadata v4의 `/task/stats`에서 Task 안의 container CPU 누적값과 memory 사용량을 합산합니다. Prepare 직후와 finish 시점에는 반드시 sampling하고, 그 사이에는 50ms를 첫 후보 간격으로 사용합니다. Report는 첫 sample과 마지막 유효 sample의 CPU 누적값 차이, 구간 최대 memory와 sampling 오류 수를 함께 반환합니다. 50ms 간격은 개발 calibration에서 overhead와 peak 누락 가능성을 확인한 뒤 최종 측정 전에 고정합니다.

여러 프로세스 사이의 요청 조정은 Redis나 DynamoDB 같은 외부 저장소를 이용해 실제 변환 담당을 하나로 정합니다. 이 경우에도 lock(잠금) 만료나 담당 프로세스 교체 중 두 작업이 겹칠 수 있으므로 S3 conditional write를 마지막 안전장치로 사용합니다.

여러 프로세스 사이의 요청 조정은 중복 변환 비용이 외부 저장소, lock 만료와 실패 복구의 복잡성보다 큰 경우에 도입합니다.

`process-singleflight`의 공유 작업은 첫 요청의 client context에서 분리되고 server-side transform timeout을 사용합니다. 각 waiter는 자신의 client context가 끝나면 독립적으로 대기를 중단할 수 있습니다. libvips의 native C 호출은 시작된 뒤 Go context로 중단할 수 없으므로 transformer는 호출 전후에 context를 확인하고, 동시에 실행할 native 변환 수를 별도로 제한합니다.

## 고정할 입력

첫 비교는 [CC0 고해상도 JPEG fixture](fixtures/README.md)와 아래 변환 옵션으로 실행합니다. Fixture는 Git LFS로 관리하며 파일의 출처, 저작자 credit, 라이선스, 크기, 해상도와 SHA-256은 fixture 문서에 고정합니다.

```text
원본:       landscape-4928x3264.jpg (4,928 × 3,264 JPEG)
크기:       640 × 640
맞춤 방식:  cover
출력 포맷:  WebP
품질:       80
```

`640 × 640`은 업로드 원본의 크기가 아니라 card/thumbnail용 파생 이미지 크기입니다. 약 16.1 megapixel인 원본을 사용해 고해상도 JPEG decode와 큰 폭의 축소를 포함합니다. 실제 변환기와 인코더 버전도 실행 결과에 남깁니다.

대표 결과를 얻은 뒤에는 같은 원본의 `1280 × 1280` 변환, 더 큰 JPEG와 알파 채널이 있는 PNG에서도 같은 경향이 나타나는지 확인합니다. 추가 조건은 sensitivity scenario로 분리하며 첫 결과의 조건을 사후에 바꾸지 않습니다.

## 고정한 구현과 실행 조건

Retained measurement 전에 다음 값을 고정했습니다.

| 항목 | 값 |
| --- | --- |
| Go | `1.26.7` |
| govips | `v2.16.0` |
| libvips | Debian package `8.16.1-1+deb13u1` (`libvips 8.16.1`) |
| Builder image | `golang:1.26.7@sha256:e30143be198ab04cf7ba25fba83ab3a692ca584c994aad0bf131fa0eb32dd8c1` |
| Runtime image | `debian:trixie-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132` |
| Container limit | 1 vCPU, 2 GiB memory |
| libvips 설정 | operation cache disabled, 한 변환의 concurrency 1 |
| Service transform 상한 | 동시에 최대 4건 |
| Request / shared transform timeout | 90초 / 60초 |
| Start-skew 허용값 | 100ms |
| Resource sampling | 10ms |
| 반복 | 성공 시나리오별 독립 cold state 10회 |

libvips의 concurrency는 한 변환 내부의 worker 수만 제한하므로 동시에 시작할 변환 건수에는 별도 semaphore를 둡니다. 4건 상한은 1 vCPU/2 GiB calibration에서 S1-100의 100회 변환을 모두 완료하면서 cgroup memory limit을 넘지 않은 값입니다. 각 trial은 별도 자식 프로세스에서 예열 후 실행해 이전 trial의 native allocator 상태가 peak memory에 남지 않게 합니다.

100-way 요청은 server와 같은 1 vCPU를 쓰는 loopback generator에서 시작 시각 차이가 최대 약 69.4ms까지 관측됐습니다. 100ms 기준은 이 generator 분포에 여유를 둔 값이며, 실제 서비스의 허용 지연이나 외부 load generator의 일반 기준이 아닙니다. 선택 과정과 제외한 dry run은 [calibration 기록](CALIBRATION.md)에 남깁니다.

## Cold 상태 만들기

각 측정 전에 다음 상태를 확인합니다.

1. 원본 파일은 준비되어 있고 해시가 맞습니다.
2. 측정할 파생 이미지는 저장소에 없습니다.
3. 이전에 진행 중이던 같은 키의 작업이 남아 있지 않습니다.
4. CDN을 거치는 실험이라면 CDN에도 해당 이미지가 없습니다.
5. 프로그램과 이미지 라이브러리의 최초 실행 비용은 다른 이미지로 미리 실행해 제거합니다. 측정할 파생 이미지는 이 과정에서 만들지 않습니다.

가능하면 실행마다 파생 이미지 키에 별도의 실험 버전을 넣어 이전 결과와 섞이지 않게 합니다. 준비 상태가 잘못된 실행은 성능 결과에서 제외하고 이유를 기록합니다.

## 요청을 동시에 시작하는 방법

부하 생성기는 먼저 필요한 수만큼 요청 실행 단위를 준비합니다. 모두 준비됐다는 것을 확인한 뒤 하나의 시작 신호를 보내 같은 URL을 거의 동시에 요청하게 합니다.

실제로는 요청이 정확히 같은 시각에 출발하지 않으므로 첫 요청과 마지막 요청이 시작된 시각 차이를 기록합니다. 이 차이를 **요청 시작 편차(request start skew, 이하 start skew)**라고 부릅니다. Start skew가 너무 큰 실행은 캐시 폭주를 제대로 만들지 못한 것으로 보고 제외합니다. 허용값은 부하 생성기 자체를 시험한 뒤 본 측정 전에 정하며, 측정 전에 그 시간만큼 기다린다는 뜻이 아닙니다.

각 실행에는 다음 정보를 남깁니다.

- 목표 동시 요청 수
- 각 요청의 실제 시작 시각
- 첫 요청과 마지막 요청 사이의 시간 차이(start skew)
- 요청 제한 시간
- 부하 생성기를 실행한 위치와 CPU·네트워크 조건

## 반복 방법

- 각 성공 시나리오는 서로 독립된 Cold 상태에서 최소 10번 실행합니다.
- 프로그램 예열을 위한 실행은 결과에서 제외합니다.
- 방식별 실행 순서를 번갈아 배치해 시간대나 실행 환경의 영향을 줄입니다.
- 각 실행 결과를 따로 보존합니다. 여러 실행의 요청을 하나로 합쳐 표본 수가 많은 것처럼 계산하지 않습니다.
- 대표값뿐 아니라 실행 사이의 차이도 함께 표시합니다.

## Phase A 정상 동작 시나리오

### 한 프로세스

| ID | 동시 요청 | 요청 합치기 | 키 개수 | 목적 |
| --- | ---: | --- | ---: | --- |
| S0 | 1 | 없음 | 1 | 이미지 한 장의 기준 시간과 자원 사용량 |
| S1-10 | 10 | 없음 | 1 | 작은 폭주의 중복 작업 |
| S1-50 | 50 | 없음 | 1 | 중간 폭주의 중복 작업 |
| S1-100 | 100 | 없음 | 1 | 큰 폭주의 중복 작업 |
| S2-10 | 10 | 프로세스 내부 | 1 | 작은 폭주에서 요청 합치기 효과 |
| S2-50 | 50 | 프로세스 내부 | 1 | 중간 폭주에서 요청 합치기 효과 |
| S2-100 | 100 | 프로세스 내부 | 1 | 큰 폭주에서 요청 합치기 효과 |

## Phase B — Phase A가 어디까지 적용되는지 확인

Phase A는 한 프로세스 안에서 같은 파생 이미지 요청을 한 번의 변환으로 합칠 수 있음을 확인했습니다. Phase B에서는 그 방식의 경계를 다음 세 가지 질문으로 나눠 확인했습니다.

1. 인기 이미지가 변환되는 동안 다른 변환 요청도 별도로 시작할 수 있는가?
2. 변환을 처음 시작시킨 클라이언트가 요청을 취소해도 기다리던 다른 클라이언트는 결과를 받을 수 있는가?
3. 같은 프로그램을 여러 프로세스로 실행하면 이미지 변환은 전체에서 몇 번 일어나는가?

모든 시나리오는 1 vCPU·2 GiB로 제한한 로컬 Docker 환경에서 10회씩 실행했습니다. Phase A의 명령과 결과를 덮어쓰지 않도록 Phase B에는 별도 명령 `e1-phase-b`와 결과 디렉터리 `results-phase-b/`를 사용했습니다.

### S3 — 인기 이미지 변환이 다른 요청의 시작을 막는가?

같은 원본에서 출력 품질만 다르게 지정해 서로 다른 파생 이미지 두 개를 만들었습니다. 품질 80 이미지는 요청이 몰리는 **인기 이미지**, 품질 79 이미지는 영향을 확인할 **비교 이미지**입니다. 원본을 같게 둔 이유는 이미지 내용과 크기의 차이를 새 변수로 추가하지 않기 위해서입니다.

시나리오 ID는 다음과 같이 읽습니다.

| 표기 | 뜻 |
| --- | --- |
| `S3` | 인기 이미지 요청이 다른 이미지 요청에 미치는 영향을 확인하는 세 번째 정상 시나리오 |
| `COLD` | 비교 이미지가 아직 저장되어 있지 않아 변환이 필요한 상태 |
| `WARM` | 비교 이미지가 이미 저장되어 있어 변환 없이 읽을 수 있는 상태 |
| `CONTROL` | 인기 이미지 요청을 빼고 비교 이미지 요청만 보내는 기준선 |

따라서 `S3-COLD-CONTROL`은 “비교 이미지가 없는 상태에서 비교 요청만 보낸 기준선”입니다. `CONTROL`이 없는 `S3-COLD`와 `S3-WARM`은 인기 이미지 90개와 비교 이미지 10개를 함께 보내는 혼합 실험입니다. 혼합 실험에서 인기 이미지는 항상 저장되어 있지 않은 상태로 시작합니다.

| ID | 동시에 보낸 요청 | 시작할 때의 저장 상태 | 확인할 것 |
| --- | --- | --- | --- |
| S3-COLD-CONTROL | 비교 이미지 10개 | 비교 이미지 없음 | 비교 이미지 변환의 기준 시간 |
| S3-COLD | 인기 이미지 90개 + 비교 이미지 10개 | 두 이미지 모두 없음 | 서로 다른 두 변환이 각각 시작되는가 |
| S3-WARM-CONTROL | 비교 이미지 10개 | 비교 이미지 있음 | 저장된 이미지 응답의 기준 시간 |
| S3-WARM | 인기 이미지 90개 + 비교 이미지 10개 | 인기 이미지만 없음 | 인기 이미지 변환 중에도 저장된 비교 이미지를 바로 읽는가 |

Cold 상태의 S3에서는 인기 이미지와 비교 이미지가 각각 한 번 변환됐고, 두 변환이 동시에 진행 중인 시점이 있었습니다. Warm 상태에서는 비교 이미지 요청 10개가 모두 저장된 결과를 읽었으며 추가 변환을 만들지 않았습니다. 즉, 서로 다른 변환 요청이 하나의 작업으로 잘못 합쳐지거나, 인기 이미지 변환이 끝날 때까지 다른 변환의 시작 자체가 막히지는 않았습니다.

다만 같은 CPU를 사용하는 영향은 남았습니다. 비교 이미지의 p99는 cold 단독 322.715ms에서 혼합 624.305ms로, 저장된 이미지의 p99는 단독 0.867ms에서 혼합 56.679ms로 늘었습니다. 이는 요청 합치기 범위는 이미지별로 나뉘어도 변환 작업과 일반 응답이 CPU까지 따로 사용하는 것은 아니라는 뜻입니다.

### F2 — 최초 요청이 취소되면 나머지 요청도 취소되는가?

`F2`는 실패와 취소 때의 동작을 확인하는 시나리오 번호입니다. 여기서는 서버 오류가 아니라 최초 클라이언트의 요청 취소를 다룹니다.

첫 요청이 이미지 변환을 시작한 뒤 같은 결과를 기다리는 요청 9개를 보냈습니다. 9개가 실제로 대기 중인 것을 확인한 다음 첫 요청만 취소했습니다.

- 취소한 첫 요청만 취소 상태로 끝났습니다.
- 기다리던 요청 9개는 모두 같은 변환 결과를 받았습니다.
- 원본 읽기, 변환과 새 파일 저장은 각각 한 번만 일어났습니다.
- 작업이 끝난 뒤 같은 이미지를 다시 요청하자 새 변환 없이 저장된 결과를 받았습니다.

이 결과는 10회 모두 같았습니다. 따라서 이미지 변환 작업의 수명은 처음 요청한 클라이언트 한 명의 연결과 분리하고, 서버가 정한 변환 제한 시간으로 관리합니다.

### S4 — 프로그램을 여러 개 실행하면 변환은 몇 번 일어나는가?

프로세스는 실행 중인 프로그램 한 개를 뜻합니다. 프로세스마다 같은 요청을 합치는 목록을 자기 메모리에 따로 가지고 있으므로, 다른 프로세스에서 이미 변환 중인 작업은 알 수 없습니다.

`S4-2`와 `S4-4`에서 마지막 숫자는 실행한 프로세스 수입니다.

이 경계를 확인하기 위해 독립된 프로세스 2개와 4개를 실행했습니다. 모든 프로세스는 같은 원본과 결과 디렉터리를 사용했습니다. 실제 로드밸런서의 우연한 분배 차이를 없애기 위해 요청 100개는 각 프로세스에 같은 수로 나눠 보냈습니다.

| ID | 프로세스 수 | 요청 분배 | 실제 변환 | 파일 저장 결과 |
| --- | ---: | --- | ---: | --- |
| S4-2 | 2 | 50개씩 | 2회 | 새 파일 1회, 이미 존재함 1회 |
| S4-4 | 4 | 25개씩 | 4회 | 새 파일 1회, 이미 존재함 3회 |

각 프로세스 안에서는 요청이 한 번의 변환으로 합쳐졌지만 프로세스 사이는 합쳐지지 않았습니다. 그 결과 프로세스 수만큼 같은 이미지가 중복 변환됐습니다. 저장할 때는 파일이 없을 때만 새로 만드는 방식을 사용해 완성된 결과 하나만 남겼지만, 이미 끝난 중복 변환의 계산 비용까지 없애지는 못했습니다.

같은 1 vCPU 제한에서 프로세스를 2개에서 4개로 늘리자 평균 p99는 646.041ms에서 1,339.364ms로, CPU 사용 시간은 674.17ms에서 1,380.54ms로 늘었습니다. 이는 여러 프로세스가 하나의 CPU 제한을 공유하는 로컬 실험 결과이며, 프로세스마다 별도 CPU를 받는 ECS 결과로 해석하지 않습니다.

### 여러 프로세스의 변환을 하나로 합칠 것인가?

Redis나 DynamoDB 같은 외부 저장소를 사용하면 여러 프로세스 중 하나만 변환하도록 조정할 수 있습니다. 이 문서에서는 이 방식을 S5라고 부릅니다. 그러나 외부 조정에는 요청 지연, 운영 비용, 잠금 만료와 장애 복구라는 새 문제가 생깁니다.

로컬 S4는 중복 변환이 생긴다는 사실만 확인했습니다. 실제 환경에서 그 비용이 외부 조정보다 큰지는 아직 알 수 없으므로 S5는 구현하지 않았습니다. 다음 단계에서는 ECS Task 2개와 4개에 실제로 요청을 보내 다음 값을 먼저 측정합니다.

- 로드밸런서가 각 Task에 나눈 실제 요청 수
- Task별 이미지 변환 횟수와 CPU·메모리 사용량
- S3에서 새 결과를 저장한 횟수와 이미 존재한 결과를 만난 횟수
- 중복 변환이 응답 시간, 오류와 비용에 미친 영향

이 값이 받아들이기 어려운 수준일 때만 프로세스 사이의 요청 합치기인 S5를 검토합니다.

## 실패와 취소 시나리오

실패와 취소 동작은 매번 같은 시점에 성공하거나 실패하도록 만든 테스트용 변환기로 확인합니다. 그래야 우연한 실행 시간 차이가 아니라 요청 처리 규칙 자체를 반복해서 검사할 수 있습니다. 실제 이미지 변환기는 정상 시나리오의 성능과 결과 이미지 확인에 사용합니다.

| ID | 단계 | 만들 상황 | 확인할 것 |
| --- | --- | --- | --- |
| F0 | Phase A 완료 | 실패 없음 | 모든 요청이 같은 이미지와 키를 받는가 |
| F1 | Phase A 완료 | 실제 변환을 맡은 작업이 오류 반환 | 기다리던 요청이 모두 끝나고 다음 요청이 다시 진행되는가 |
| F2 | Phase B 완료 | 최초 클라이언트가 요청 취소 | 기다리던 다른 요청과 이미지 변환은 계속되는가 |
| F3 | 후속 후보 | 서버의 변환 제한 시간 초과 | 기다리던 요청을 끝내고 재시도 폭주를 막을 수 있는가 |
| F4 | Phase B 로컬 완료 | 여러 프로세스가 같은 파생 이미지 저장 | 완성된 파일 하나만 남고 불완전한 파일은 보이지 않는가 |
| F5 | 후속 후보 | 변환 중인 프로세스 종료 | 다른 프로세스가 복구하고 재시도가 다시 폭주하지 않는가 |

실패 시나리오에서 모든 요청이 성공할 필요는 없습니다. 다만 정한 시간 안에 끝나야 하며, 계속 남는 작업이나 제한 없는 재시도가 없어야 합니다.

F2의 실행 순서와 결과는 위의 [최초 요청이 취소되면 나머지 요청도 취소되는가?](#f2--최초-요청이-취소되면-나머지-요청도-취소되는가)에서 설명합니다. F3과 F5는 아직 실행하지 않았습니다.

## Phase B 완료 확인

- Phase A의 기존 실행 명령, 원자료 검증과 결과 보고서를 그대로 다시 만들 수 있습니다.
- S3의 네 가지 비교, F2, S4-2와 S4-4를 각각 유효하게 10회 측정했습니다.
- 각 요청이 인기/비교 이미지 중 무엇인지, 최초/대기 요청 중 무엇인지, 어느 프로세스로 보내고 실제 어디서 처리했는지를 원자료에 기록했습니다.
- 분석 명령이 요청별 결과, 실행별 요약과 프로세스별 계측값이 서로 맞는지 자동으로 검사합니다.
- S3에서 서로 다른 변환은 각각 시작됐고, 이미 저장된 비교 이미지는 추가 변환 없이 응답했습니다.
- F2에서 최초 요청의 취소는 이미지 변환과 다른 요청을 취소하지 않았고, 후속 요청도 저장된 결과를 받았습니다.
- S4에서 모든 프로세스가 같은 완성 이미지를 응답했고 새 결과 파일은 하나만 만들어졌습니다.
- 로컬 결과만으로 여러 프로세스 사이의 요청 합치기를 도입하지 않고, 실제 ECS/S3 측정을 먼저 하기로 결정했습니다.

위 항목은 [Phase B 결과 리포트](../../reports/e1-cache-stampede/PHASE-B.md)와 최종 원자료에서 모두 확인했습니다. 7개 시나리오를 10회씩 실행한 총 70회가 모두 유효했고 원자료 교차검증도 통과했습니다.

## 기록할 메트릭

```text
media_derivative_requests_total{result="hit|miss"}
media_original_reads_total{result="success|error"}
media_transform_attempts_total{result="success|error|timeout"}
media_transform_duration_seconds
media_transform_inflight
media_requests_coalesced_total
media_derivative_publish_attempts_total{result="created|existing|error"}
media_request_duration_seconds{cache="edge|derivative|miss",result="..."}
```

각 요청 결과에는 다음 항목을 남깁니다.

```text
실행 ID, 반복 ID, 시나리오, 요청 ID, 이미지 키, ECS Task ID,
시작 시각, 종료 시각, 응답 시간, HTTP 상태,
응답 이미지 해시, 오류 종류, 다른 요청과 합쳐졌는지 여부
```

CPU 사용 시간, 최대 메모리, 네트워크 전송량과 S3 요청 수도 함께 수집합니다. 수집 간격과 사용한 도구는 실행 정보에 기록합니다.

## 결과 파일

```text
experiments/e1-cache-stampede/
  README.md
  fixtures/                    테스트 이미지의 사용 조건과 해시
  results/<run-id>/
    run.json                   실행 환경과 조건
    trials.csv                 각 반복의 유효 여부와 요약
    requests.csv               요청별 결과
    resources.csv              시간대별 CPU·메모리 사용량
    metrics.prom               메트릭 원본
    logs.jsonl                 요청을 연결해 볼 수 있는 로그
    analysis/
      analysis.json            raw 교차 검증 결과와 집계 조건
      summary.csv              trial 경계를 보존한 기반 표
      transform-count.svg      실제 변환 횟수 chart
      latency-error.svg        latency percentile와 오류율 chart

cmd/e1-runner/                 실행과 분석 CLI
internal/e1runner/             workload, raw writer, 검증과 chart 코드
```

그래프는 원본 자료에서 다시 만들 수 있어야 합니다. 결과 파일이 너무 크면 별도 보관 방법을 정하되, 리포트의 숫자를 확인하는 데 필요한 자료와 코드는 공개합니다.

## 로컬 재현

Docker와 Git LFS가 필요합니다. Retained result는 깨끗한 commit에서 만들며, 같은 image 안의 동일 binary가 `none`과 `process-singleflight`를 번갈아 실행합니다. 아래 명령의 `run`은 완료 후 raw 파일을 교차 검증하고 두 SVG도 자동 생성합니다.

```bash
git lfs pull
test -z "$(git status --porcelain)"

commit="$(git rev-parse HEAD)"
image="content-serving-e1:${commit}"
docker build --target experiment --build-arg "GIT_COMMIT=${commit}" -t "${image}" .
image_id="$(docker image inspect --format '{{.Id}}' "${image}")"

mkdir -p experiments/e1-cache-stampede/results
docker run --rm --cpus=1 --memory=2g \
  --mount "type=bind,source=${PWD}/experiments/e1-cache-stampede/results,target=/results" \
  "${image}" run \
  --run-id "retained-${commit}" \
  --container-image "${image}#${image_id}"
```

기존 raw result에서 검증과 chart만 다시 실행할 수 있습니다.

```bash
docker run --rm \
  --mount "type=bind,source=${PWD}/experiments/e1-cache-stampede/results,target=/results" \
  "${image}" analyze --run-dir "/results/retained-${commit}"
```

Docker build 단계가 `go test ./...`와 `go vet ./...`를 실행합니다. 결과의 `run.json`에는 commit, image 식별자, fixture/library/resource/timeout 조건과 실제 실행 명령이 들어갑니다. `analysis.json`의 `raw_validation`이 `passed`가 아니거나 invalid trial이 있으면 대표 결과로 사용하지 않습니다.

### Phase B 다시 실행하기

Phase B는 Phase A 결과를 덮어쓰지 않도록 별도 실행 파일과 결과 디렉터리를 사용합니다. 아래 명령은 S3의 네 가지 비교, 최초 요청 취소 F2, 프로세스 2/4개의 S4를 각각 10회 실행합니다. 실행이 끝나면 원자료가 서로 맞는지 검사하고 결과 그래프 세 장을 만듭니다.

```bash
commit="$(git rev-parse HEAD)"
image="content-serving-e1-phase-b:${commit}"
docker build --target experiment-phase-b --build-arg "GIT_COMMIT=${commit}" -t "${image}" .
image_id="$(docker image inspect --format '{{.Id}}' "${image}")"

mkdir -p experiments/e1-cache-stampede/results-phase-b
docker run --rm --cpus=1 --memory=2g \
  --mount "type=bind,source=${PWD}/experiments/e1-cache-stampede/results-phase-b,target=/results-phase-b" \
  "${image}" run \
  --run-id "retained-${commit}" \
  --container-image "${image}#${image_id}"
```

이미 저장한 Phase B 원자료만 다시 검사하고 그래프를 만들 수도 있습니다.

```bash
docker run --rm \
  --mount "type=bind,source=${PWD}/experiments/e1-cache-stampede/results-phase-b,target=/results-phase-b" \
  "${image}" analyze --run-dir "/results-phase-b/retained-${commit}"
```

Phase B 결과에는 실행 조건을 담은 `run.json`, 반복별 요약 `trials.csv`, 요청별 결과 `requests.csv`, 자원 사용량 `resources.csv`, 프로세스별 계측값 `metrics.csv`와 취소 순서를 기록한 `events.csv`가 들어갑니다. `analysis/`에는 교차검증 결과, 집계 표와 그래프 세 장이 만들어집니다. 개발 중 시험 실행이나 calibration 값은 최종 결과 근거로 사용하지 않습니다.

## 결과에서 보여줄 것

첫 리포트에는 다음 네 가지를 담습니다.

1. 동시 요청 수에 따라 실제 변환 횟수가 어떻게 늘어나는지
2. 요청을 합치기 전후의 p50, p95, p99와 오류율
3. 전체 CPU 사용 시간과 최대 메모리 변화
4. leader가 실패한 시점부터 waiter 종료와 복구까지의 흐름

그래프에는 단위, 반복 횟수, CPU·메모리 제한과 사용한 이미지를 표시합니다.

## 결과를 보고 내릴 결정

프로세스 내부의 요청 합치기는 다음 조건을 모두 만족하면 사용합니다.

- 성공한 실행에서 같은 키의 실제 변환이 한 프로세스 안에서 한 번으로 줄어듭니다. 로컬 다중 프로세스의 경계는 Phase B에서 확인했으며 실제 ECS Task는 아직 측정하지 않았습니다.
- 동시 요청이 하나일 때 반복해서 눈에 띄는 성능 저하가 생기지 않습니다.
- 다른 이미지 키를 하나의 잠금으로 막지 않습니다.
- 실패 시나리오가 요청 제한 시간 안에 끝나고 다음 요청이 진행됩니다.
- 요청을 합치기 전후의 결과 이미지가 같습니다.

프로세스 사이의 요청 조정은 다음 문제가 실제로 나타날 때만 구현합니다.

- 여러 ECS Task의 중복 변환 때문에 CPU·메모리가 포화되거나 오류가 늘어납니다.
- 저장 충돌만 막아서는 계산 낭비나 응답 지연을 받아들이기 어렵습니다.
- 예상 요청량과 변환 비용을 계산했을 때 외부 조정 기능의 비용보다 중복 비용이 큽니다.

## 정확성 확인

- 의미가 같은 변환 요청은 같은 파생 이미지 키와 같은 결과를 만듭니다.
- 다른 변환 요청은 서로 합쳐지지 않습니다.
- 저장된 이미지는 정상적으로 열리고 요청한 크기와 포맷을 가집니다.
- 실패하거나 중단된 변환의 불완전한 파일은 조회되지 않습니다.
- 요청이 취소된 뒤에도 계속 남는 goroutine이나 진행 중 항목이 없습니다.
- 메트릭에 기록된 변환·읽기·저장 횟수가 테스트용 변환기와 저장소의 실제 호출 횟수와 같습니다.

## Phase A 완료 조건

- S0, S1-10/50/100과 S2-10/50/100을 같은 commit/image에서 각각 유효 trial 10회 측정합니다.
- F1에서 leader 오류를 공유한 waiter가 제한 시간 안에 끝나고 후속 요청이 복구됨을 확인합니다.
- Counter, 요청 결과와 변환 호출 기록을 자동 교차 검증합니다.
- 원본 측정 자료에서 기반 표와 두 그래프를 다시 만들 수 있습니다.
- 프로세스 내부 동일 요청 합치기의 채택 여부와 적용 범위를 수치로 결정합니다.
- Invalid trial, 합성 환경의 한계와 확인하지 못한 범위를 공개합니다.

위 조건은 [Phase A 결과 리포트](../../reports/e1-cache-stampede/README.md)와 retained raw에서 모두 충족했습니다. Phase A 재현 경로는 Phase B와 분리해 유지하며 후속 경계는 [Phase B 결과](../../reports/e1-cache-stampede/PHASE-B.md)에 기록했습니다.
