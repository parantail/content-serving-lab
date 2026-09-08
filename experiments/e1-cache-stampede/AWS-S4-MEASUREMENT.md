# AWS S4 자원 계측 계약

상태: **측정 조건·retained 실행 경로 구현 — 실제 retained 수행 전**. 실제 Fargate 범위와 독립 CPU counter 대조는 [진단 원자료와 결과](../../reports/e1-cache-stampede/aws-s4-resource-diagnostic/README.md)로 확인했다. 기본 모드는 calibration이며 retained는 명시적으로 선택한다. 이 문서는 최종 측정 완료 기록이 아니다.

## 지표의 의미

| 지표 | 계산·범위 | 해석하지 않는 것 |
| --- | --- | --- |
| Task 행 `cpu_usage_nanos` | prepare의 첫 sample부터 drain 후 finish의 마지막 유효 sample까지 container-visible cgroup 누적 CPU 차이 | CPU 사용률(%), 변환 함수만의 CPU, 청구 vCPU 시간 |
| Trial CPU | 참여 Task 행 CPU delta 합계 | 모든 Task의 동일한 시작·종료 시각에 대한 적분 |
| Task 행 `peak_memory_bytes` | 해당 구간에서 읽은 cgroup memory usage의 최댓값 | 정확한 순간 peak, 프로세스 RSS, trial로 인해 추가된 메모리 |
| Trial `peak_memory_bytes` | 참여 Task별 관측 최댓값 중 가장 큰 값 (`max`) | Task 전체 동시 memory 합계, Task별 peak 합계 |
| `transform_duration_nanos` | 변환 호출의 경과 시간 합계 | CPU 시간 |

CPU 측정 구간에는 HTTP·S3 client·제어 endpoint·sampler·runtime 작업이 포함될 수 있다. 준비/종료 endpoint에 각 Task가 도달하는 시각도 다르다. 무부하 값을 사후 차감하거나 CPU를 변환 수로 나눠 순수 libvips CPU라고 주장하지 않는다.

Memory는 allocator 잔류, page cache와 기존 사용량을 포함할 수 있다. 반복 간 프로세스를 재시작하지 않으므로 첫 trial과 이후 trial은 memory 기준선이 같다는 보장이 없다. 범위가 다른 cgroup v1/v2 또는 legacy metadata run을 같은 자원 시계열로 합치지 않는다.

## 최종 측정 조건

- 환경: `ap-northeast-2`, Media 1/2/4 Task 각각 1 vCPU·2 GiB, 별도 runner 1 vCPU·2 GiB.
- Workload: 고정 fixture와 canonical transform, 100-request cold burst, 홀수 1→2→4·짝수 4→2→1 순서, 각 유효 10회.
- 시작 시각 차이 상한: **50ms**. 하나의 barrier로 시작한 요청들의 실제 시작 시각 차이가 이를 넘으면 trial을 제외한다. 기존 calibration의 gate=0은 유지하고 원자료를 소급 변경하지 않는다. 최종 결과를 보고 상한을 완화하지 않는다.
- 자원 sampling: **50ms**, prepare/finish 경계 sample 포함. 실제 간격은 scheduler와 파일 읽기에 따라 달라지므로 `resources.csv` timestamp로 분포와 최댓값을 보고한다. 짧은 memory spike를 놓칠 수 있다. 간격을 지키지 못한 sample을 지우거나 보간하지 않는다.
- Timeout: 요청 90초, 제어 30초, 제어 polling 100ms. 현재 CLI 기본값이며 retained 명령에는 명시한다.
- Source: 이번 retained는 검증한 **`cgroup-v1-container-visible`**만 허용한다. Source 누락·metadata 방식·v2·혼합 source는 최종 계약 부적합이다. Source 누락 legacy raw의 산술 검증 성공을 정확도 검증으로 취급하지 않는다.

### 판단 범위와 중단 기준

50ms sampling의 합성 무부하 추가 CPU는 약 1초당 5.8~7.5ms였고, busy 처리량 차이는 반복별 -1.46/-2.68/-0.67%였다. 이 비용을 포함한 시스템 관측값으로 1/2/4 Task의 중복 작업을 비교한다. 순수 변환 CPU나 overhead를 제거한 latency가 목적이 아니므로 무부하 값을 사후 차감하지 않는다. 이 작은 진단 표본으로 CPU 오차 ±1% 같은 보증이나 임의의 합격선을 만들지 않는다. 작은 성능 차이의 원인 판정은 별도 민감도 실험 없이는 보류한다.

CPU 읽기 오류·역행·변환 성공에도 delta=0, source/조건 불일치 또는 scope 대조 실패 시 최종 결과로 승인하지 않는다. 기존 요청·응답 hash·저장소·Task 집합·cold 상태 검증도 유지한다. 각 scenario 10회씩 총 30회를 실행하고 하나라도 invalid이면 제외 사유와 전체 raw를 보존하되 유효 10회 완료로 선언하지 않는다. 좋은 결과가 나올 때까지 trial을 자동 보충하거나 기존 calibration을 retained로 바꾸지 않는다.

Memory는 sampled maximum만 보고하며 실제 peak 대비 오차 상한을 주장하지 않는다. CPU 범위·process 대조는 새 checkpoint의 동일 image/runtime/resource 구성에서 본 측정 전 다시 확인한다. 최종 문서의 조건과 실행 입력을 고정한 clean checkpoint가 있어야 AWS 본 측정을 시작할 수 있다.

## 로컬 대조 검증

Opt-in Linux 진단은 1초 무부하/CPU 부하 × sampler off/on을 각 3회, 정순·역순으로 실행한다. cgroup CPU와 `getrusage(RUSAGE_SELF)`의 user+system CPU를 나란히 기록한다. 포화 부하에서는 CPU 시간이 거의 같아도 처리량이 달라질 수 있으므로 작업 반복 수도 기록한다. 최상의 반복만 선택하지 않는다.

공개 저장소 root에서 PowerShell 7로 실행한다. AWS credential과 network는 사용하지 않는다.

```powershell
docker build --target build --build-arg GIT_COMMIT=development -t e1-resource-check .
docker run --rm --network none --cpus=1 --memory=2g `
  -e E1_RESOURCE_CALIBRATION=true e1-resource-check sh -c `
  'go test -c -o /tmp/resource-calibration.test ./internal/e1awss4 && exec /tmp/resource-calibration.test -test.v -test.run ^TestResourceCalibration$ -test.count=1'
```

진단은 측정 오류·CPU 역행·부하 구간에서 두 CPU counter가 증가하지 않는 경우 실패한다. 임의의 오버헤드 합격선을 실행 결과에 맞춰 만들지 않는다. 로컬 통과는 Fargate mount 범위나 오버헤드의 증명이 아니다.

## 최종 checkpoint 전 남은 검증

### Fargate 진단 실행

`E1_RESOURCE_DIAGNOSTIC=true`로 service binary를 실행하면 HTTP 서버 대신 12개 진단 구간과 전후 cgroup 범위 요약을 JSON lines로 출력하고 종료한다. 프로세스 membership/mount root 비교, 현재 프로세스 포함 여부, 직접 멤버 수와 숨겨진 PID·하위 group 수를 기록한다. 원본 경로나 명령행은 출력하지 않는다. 하위 group 또는 숨겨진 멤버가 있으면 단일 프로세스 범위라고 단정하지 않는다. 누적 CPU와 process CPU의 가까운 수치만으로 범위가 동일하다고 증명하지도 않는다.

로컬에서는 `service` image에 위 환경 변수와 `--cpus=1 --memory=2g --network none`을 지정해 같은 모드를 검증할 수 있다. AWS에서는 clean checkpoint의 기존 bootstrap/plan/apply 후 `deploy/e1-aws-s4`에서 다음을 실행한다.

진단은 reader directory의 symbolic link를 해석한 뒤 mountinfo의 escape된 경로를 복원하여 가장 구체적인 mount와 비교한다. cgroup v1은 controller를, v2는 filesystem type을 확인한다. `path_alias_resolved`는 별칭 해석으로 경로가 달라졌는지만 나타내며 원본 경로는 노출하지 않는다. `mount_found=false`이면 mount root 관련 false 값은 범위 판정의 근거가 아니다. 경로 해석 실패는 경로를 숨긴 오류로 종료한다. 로컬 alias 회귀 test 통과와 실제 Fargate mount 대응 확인은 별개다.

```powershell
.\scripts\resource-diagnostic.ps1
.\scripts\destroy.ps1
```

진단 Task는 Media Service와 동일한 task definition/image/user/CPU/memory를 사용하지만 ECS Service에는 등록하지 않는다. Result bucket 쓰기 권한을 추가하지 않고 CloudWatch의 해당 Task stream을 로컬 `recovered-results/<deployment-id>/resource-diagnostic/`에 회수한다. 실행 제한은 5분, 프로그램 내부 제한은 1분이다. 오류 시 재실행하지 말고 회수 가능한 로그를 확인한 뒤 destroy한다. 기존 watchdog/destroy도 진단용 `startedBy` Task를 정리한다. 진단 성공은 12구간의 실행·회수 성공이며 정확도 승인과 별개다.

### 실행 gate

1. Runner와 analyzer는 retained의 `measurement_contract=e1-aws-s4-retained-v1`, 50ms sampling·skew, 90초/30초/100ms timeout/poll, 고정 fixture·region·10회를 검증한다. Controller는 실제 sampler 설정을 `resource_sample_gap_ms`로 응답하고 Task raw에도 기록한다. 누락·v2·혼합 source·Task 크기 불일치를 거부한다. 기존 calibration raw의 새 필드 누락은 허용하되 retained로 승격하지 않는다.
2. Bootstrap의 `-RunMode retained`와 실행의 `-RunMode retained`가 일치해야 한다. Run ID는 `retained-<commit 앞 12자>`이고 calibration과 분리된다. 로컬/원격 결과가 있으면 실행하지 않는다. Runner가 S3 `_reservation.json`을 `If-None-Match: *`로 선점하여 동시 실행을 막는다. 실패한 예약을 지우고 재시도하지 않는다. 같은 run 내부의 raw/analysis 업로드만 이어서 수행한다.
3. 로컬 Go·Terraform·PowerShell 검증 후 clean checkpoint를 커밋한다. 동일 image digest의 Fargate 진단과 retained 실행을 분리한다. 실행 스크립트는 회수한 진단의 source·scope·commit·image·platform·자원 크기·12구간을 검사한다. 비용 상한과 자동 제거 제한은 배포 전에 다시 확인하며 실행 시작 때 최소 15분을 남긴다.
4. 30개 유효 trial을 회수·독립 재분석하고 모든 invalid와 sample 간격을 함께 보고한다. Analyzer의 raw validation과 최종 완료는 별개이며 runner는 invalid가 하나라도 있으면 raw/분석 업로드 후 실패로 종료한다. 회수 뒤 즉시 destroy와 독립 잔여 검사를 수행한다.

근거: [Linux cgroup v2](https://docs.kernel.org/admin-guide/cgroup-v2.html)는 계층적 cgroup 범위와 CPU/memory counter를 정의한다. [getrusage(2)](https://www.man7.org/linux/man-pages/man2/getrusage.2.html)의 `RUSAGE_SELF`는 프로세스 전체 thread 사용량이며 cgroup 전체와 범위가 다르다.
