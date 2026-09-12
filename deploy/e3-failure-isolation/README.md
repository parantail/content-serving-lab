# E3 one-shot Fargate

서울의 Linux amd64 Task 하나에서 E3 runner가 Media Service와 세 request stream을 같은 프로세스에 띄우고, 원본·파생 이미지 저장소만 실제 S3를 사용한다. ECS Service·ALB·NAT 없이 1 vCPU·2 GiB를 고정한다. [실험 계약](../../experiments/e3-failure-isolation/README.md), [비용 계산](COST.md)을 먼저 확인한다.

상태: **실행·정리 완료 (2026-09-11)**. Deployment `e8c21674a86f`는 12:49 UTC bootstrap, 12:53 apply(20 create), calibration 2회, 본 측정 1회 뒤 13:58~13:59 UTC에 21개 자원을 제거했고 Terraform state와 서비스 API 잔여가 모두 0임을 확인했다. 첫 calibration(`aws-calib-e8c21674a86f`, image `e8c2167`)은 로컬과 같은 rate에서 Fargate CPU가 포화되어 T0부터 miss가 timeout됐고, 두 번째 calibration(`aws-calib2-…`)은 rate를 낮춘 뒤 T0 trial이 공유 버킷의 이전 파생 이미지 때문에 `miss_stream_served_as_hit`로 무효였다. 이를 고친 `3040e3d`의 image(`sha256:49937a3f…`)로 task definition을 revision 2로 바꾸고 `aws-measure-3040e3db9be0`(T0·T2·T4 × 세 모드 × 2회, 18 trial 모두 유효, Task 45.6분)를 실행했다. 반복은 2시간 deadline 안에 3회가 들어가지 않아 2회로 줄였다. 원자료는 [results-aws](../../experiments/e3-failure-isolation/results-aws/)에 있고 비용 관측은 [cost-observation.json](../../experiments/e3-failure-isolation/results-aws/cost-observation.json)이다. Fargate에서는 runner의 cgroup 판독이 실패해 AWS 결과의 CPU·메모리 열은 비어 있다.

로컬 Phase A와 같은 요청·장애 경로에 실제 S3 저장소를 연결한다. 실행 환경에 맞춰 AWS의 stream rate와 실제 반복 횟수는 낮췄다. hit 요청은 S3 GetObject, miss 요청은 S3 원본 GetObject와 `If-None-Match: *` 파생 PutObject를 거친다. ALB가 없으므로 health check 반응은 보지 않는다. Runner는 실행 후 결과 디렉터리 전체를 같은 버킷의 `experiments/e3-failure-isolation/results-aws/<run-id>/`에 조건부 업로드한다.

아래 명령은 공개 저장소 root의 PowerShell 7에서 실행한다. AWS CLI 2.32+, Terraform 1.15–1.16, Docker와 유효한 sandbox `aws login` 세션이 필요하다. E1의 checked CLI·자격/예산 검사·로컬 watchdog 패턴과 E2의 one-shot 배포 스크립트를 재사용하며 E1/E2 state는 변경하지 않는다.

```powershell
terraform -chdir=deploy/e3-failure-isolation init -backend=false -lockfile=readonly
terraform -chdir=deploy/e3-failure-isolation validate
terraform -chdir=deploy/e3-failure-isolation test
# 로컬 Phase A 측정이 끝난 clean commit에서 실행한다.
./deploy/e3-failure-isolation/scripts/bootstrap.ps1 -DeploymentId <unique-id> -ExpectedCostUsd 1.20
./deploy/e3-failure-isolation/scripts/plan.ps1
# local/plan-review.json과 terraform show를 검토하고 저장된 plan SHA256을 사용한다.
./deploy/e3-failure-isolation/scripts/apply.ps1 -ReviewedPlanSHA256 <sha256>
./deploy/e3-failure-isolation/scripts/run.ps1 -Mode calibrate -RunId <unique-calibration-id>
# calibration 2 trial이 유효하면 본 측정을 시작한다.
./deploy/e3-failure-isolation/scripts/run.ps1 -Mode measure -RunId <unique-measurement-id> -Repetitions 2
./deploy/e3-failure-isolation/scripts/destroy.ps1
```

Bootstrap은 실제 생성 전 2시간 deadline과 숨겨진 PowerShell watchdog를 시작한다. ECR 하나만 생성하는 저장된 bootstrap plan을 검사하고 `experiment-e3` target image를 immutable commit tag로 push한다. 전체 plan은 `image@sha256`를 사용한다. 배포/Task 상세·plan·회수 자료는 Git 제외 `local/`에 보관한다. Terraform state, runtime 설정과 watchdog 기록도 Git/Docker context에서 제외한다.

`run.ps1 -Mode calibrate`는 baseline × none/transform-timeout 2 trial을, `-Mode measure`는 기본값으로 `baseline,bounded-wait,kill-switch` × `none,transform-timeout,slow-original` × 3회의 27 trial을 실행한다. Task 감독 시간은 trial 수 × 2.75분 + 8분이며 인프라 deadline을 우선 적용한다. 남은 시간이 감독 시간 + 10분보다 짧으면 실행을 거부한다.

`destroy.ps1`은 전용 cluster의 Task를 종료하고 S3 결과를 회수한 후 Terraform destroy를 실행한다. 회수 오류가 있어도 deadline 정리는 계속하며 오류를 기록한다. State 외에 EC2 network·S3·ECR·IAM·Logs·ECS API의 잔여 수를 확인한다. 종료한 Task 이력과 inactive task definition은 실행 중 자원이 아니며 AWS 보존 이력으로 남을 수 있다.

watchdog는 PC와 AWS 인증에 의존한다. 이를 AWS 자체 예약 정리로 표현하지 않는다. 인증 문제로 cleanup이 실패하면 즉시 사용자 수동 대응이 필요하다.

## 회수 결과의 공개 사본

자원 제거와 잔여 검사를 확인한 뒤, 회수한 개별 run 디렉터리를 다음 명령으로 새 경로에 내보낸다. Terraform 작업 디렉터리나 ECS launch request를 입력으로 사용하지 않는다.

```powershell
python experiments/e3-failure-isolation/export_aws.py <recovered-run-directory> <fresh-public-run-directory>
python -m unittest discover -s experiments/e3-failure-isolation -p test_export_aws.py
```

`run.json`의 ECR 주소는 registry를 제외한 image 이름과 digest로, hostname과 bucket 이름은 표시용 별칭으로 바꾼다. 원본·파생 저장소가 같은 bucket인지도 유지한다. 실행 명령의 별칭은 재실행 환경에서 만든 실제 bucket·image 주소로 대체해야 한다. Commit·image digest·fixture hash·측정 조건은 그대로이며 요청·자원·분석 파일은 byte 단위로 보존한다. 내보내기는 gzip 내부를 포함해 남은 식별자와 credential을 검사하고, 예상하지 못한 파일이나 기존 출력 디렉터리가 있으면 중단한다.
