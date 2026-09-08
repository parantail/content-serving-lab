# AWS S4 자원 계측 계약 검토

상태: **계약 초안 — retained 실행 승인 전**. 배포 workflow는 아직 calibration을 실행한다. 이 문서만으로 최종 측정이나 실제 Fargate 계측 정확도 검증이 완료된 것은 아니다.

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

## 측정 조건 후보

- 환경: `ap-northeast-2`, Media 1/2/4 Task 각각 1 vCPU·2 GiB, 별도 runner 1 vCPU·2 GiB.
- Workload: 고정 fixture와 canonical transform, 100-request cold burst, 홀수 1→2→4·짝수 4→2→1 순서, 각 유효 10회.
- 시작 시각 차이 상한: **50ms 후보**. Retained 이전 checkpoint에서 명시적으로 고정해야 한다. 기존 calibration의 gate=0은 유지하고 원자료를 소급 변경하지 않는다.
- 자원 sampling: **50ms 후보**, prepare/finish 경계 sample 포함. 실제 간격은 scheduler와 파일 읽기에 따라 달라지므로 `resources.csv` timestamp로 분포와 최댓값을 보고한다. 짧은 memory spike를 놓칠 수 있다.
- Timeout: 요청 90초, 제어 30초, 제어 polling 100ms. 현재 CLI 기본값이며 retained 명령에는 명시한다.
- Source: `resource_source`를 기록하고 직접 cgroup source만 비교한다. Source 누락 legacy raw의 산술 검증 성공을 정확도 검증으로 취급하지 않는다.

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

```powershell
.\scripts\resource-diagnostic.ps1
.\scripts\destroy.ps1
```

진단 Task는 Media Service와 동일한 task definition/image/user/CPU/memory를 사용하지만 ECS Service에는 등록하지 않는다. Result bucket 쓰기 권한을 추가하지 않고 CloudWatch의 해당 Task stream을 로컬 `recovered-results/<deployment-id>/resource-diagnostic/`에 회수한다. 실행 제한은 5분, 프로그램 내부 제한은 1분이다. 오류 시 재실행하지 말고 회수 가능한 로그를 확인한 뒤 destroy한다. 기존 watchdog/destroy도 진단용 `startedBy` Task를 정리한다. 진단 성공은 12구간의 실행·회수 성공이며 정확도 승인과 별개다.

### 승인 조건

1. Fargate에서 실제 reader 경로와 `/proc/self/cgroup`, mount root 및 process membership을 대조한다. 공유 parent나 Task helper 포함 여부를 확인하고 sanitized source/scope 증거를 남긴다.
2. 동일 Fargate 자원 크기에서 무부하/CPU 부하·sampler off/on과 process CPU 교차검증을 수행한다. 로컬 v2 결과를 Fargate v1에 외삽하지 않는다. 필요하면 실제 이미지 workload의 sampling 민감도도 비교한다.
3. 결과를 검토한 후 sampling과 start-skew·허용할 계측 오차/제약을 최종 문서에 고정하고, retained source/조건 검증을 구현한다. 현재 `run-task.ps1`의 Terraform task definition은 calibration 전용이므로 retained 실행 모드와 별도 run ID를 준비해야 한다.
4. 최종 checkpoint의 clean commit 및 동일 이미지 digest에서 retained를 실행한다. Calibration raw를 retained로 이름만 바꿔 사용하지 않는다.

근거: [Linux cgroup v2](https://docs.kernel.org/admin-guide/cgroup-v2.html)는 계층적 cgroup 범위와 CPU/memory counter를 정의한다. [getrusage(2)](https://www.man7.org/linux/man-pages/man2/getrusage.2.html)의 `RUSAGE_SELF`는 프로세스 전체 thread 사용량이며 cgroup 전체와 범위가 다르다.
