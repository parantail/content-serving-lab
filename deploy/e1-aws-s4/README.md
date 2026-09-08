# E1 AWS S4 Terraform

이 디렉터리는 E1에서 ECS Task 1/2/4개가 같은 cold image request를 받을 때 프로세스별 중복 변환과 S3 조건부 저장 경쟁을 측정하는 일회성 `ap-northeast-2` 환경을 만듭니다. Provider schema와 mock plan/apply 계약, 실제 AWS 배포·제거 및 잔여 실행 자원 검사를 확인했습니다. [Retained 결과](../../reports/e1-cache-stampede/AWS-S4.md)는 30개 유효 trial과 재분석 근거를 포함합니다.

## 고정 구성

- 2개 public subnet, Internet Gateway, NAT Gateway 없음, 같은 Region S3 gateway endpoint
- S3 endpoint policy는 실험 버킷 접근과 ECR 이미지 pull에 필요한 해당 Region의 `prod-<region>-starport-layer-bucket/*` 읽기를 허용합니다.
- 내부 ALB와 8081/8082/8084 listener, non-sticky round robin target group 3개
- 동일 Media Service image의 ECS Service 3개: desired count 1/2/4, Task마다 1 vCPU·2 GiB
- 일회성 부하 생성기 Fargate Task 1개: 1 vCPU·2 GiB
- account ID의 단방향 namespace로 전역 이름 충돌을 피한 Original/Derivative/Result S3 bucket, account·bucket public access 차단과 TLS-only policy
- immutable Media/Runner ECR repository, 1일 CloudWatch Logs 보존
- execution/media/runner IAM role 분리와 고정 object·cluster 작업만 허용하는 task policy. Media GET과 runner HEAD의 정확한 miss 판정을 위해 두 role에만 Derivative bucket의 `ListBucket` 허용
- 모든 자원에 `Project=content-serving-lab`, `Experiment=e1-aws-s4`, `ExpiresAt` tag

Media Service는 `STORAGE_BACKEND=s3`, `ORIGINAL_BUCKET`, `DERIVATIVE_BUCKET` 환경 변수로 실제 S3 store를 선택합니다. Derivative bucket policy는 `If-None-Match: *`가 없는 write와 다른 조건값의 write를 거부합니다.

## 안전 계약

PowerShell 7에서 실행합니다. `preflight.ps1`은 다음 조건을 모두 확인합니다.

- AWS CLI 2.32.0 이상과 `aws login` 또는 문서화한 `credential_process`의 임시 자격증명
- 전용 IAM User의 MFA와 `AdministratorAccess`
- `ap-northeast-2`, 사용 가능한 두 AZ, Fargate On-Demand quota 8 vCPU 이상
- sandbox account의 월간 US$10 Cost Budget과 실제 비용 50/80/100% 알림 subscriber
- account-level S3 Block Public Access 네 항목
- bootstrap 전 같은 실험 tag의 자원이 0개이고, full plan 전 현재 배포의 ECR repository만 정확히 2개임

Budget은 지출을 차단하지 않습니다. 첫 ECR bootstrap 직전부터 최대 2시간 deadline을 기록하고 숨김 PowerShell watchdog을 시작합니다. 기한에 도달하면 실행 중인 one-shot runner를 중단하고 가능한 Result object를 로컬로 회수한 뒤 `terraform destroy`를 실행하며, state와 AWS의 ECR/S3/IAM/ELB/ECS/VPC/Logs/tagged resource를 다시 확인합니다. 운영자가 먼저 끝냈다면 `destroy.ps1`을 직접 실행하며 대기 중인 watchdog도 함께 종료됩니다.

Watchdog은 배포를 시작한 Windows host에 의존합니다. 해당 host를 종료하거나 재부팅하면 2시간 제거 보장이 사라지므로 실험 중에는 전원을 유지해야 합니다. 중단이 있었다면 다시 측정하지 말고 즉시 `destroy.ps1`을 실행합니다.

Terraform state와 plan에는 account ID, ARN과 실제 resource name이 포함될 수 있으므로 저장소에 올리지 않습니다. Budget 이름·알림 주소·credential도 Terraform variable, state, 출력 파일에 기록하지 않습니다.

## 실행 순서

공개 checkpoint commit이 clean 상태일 때 그 commit의 앞 12자를 deployment ID로 사용합니다. `ExpectedCostUsd`는 실행 전에 검토한 보수적 상한이며 반드시 US$10보다 작아야 합니다.

```powershell
$commit = (git rev-parse HEAD).Trim()
$deploymentId = $commit.Substring(0, 12)
Set-Location deploy/e1-aws-s4

.\scripts\bootstrap-images.ps1 `
  -DeploymentId $deploymentId `
  -ExpectedCostUsd 5.00

.\scripts\plan.ps1
.\scripts\apply.ps1
```

`bootstrap-images.ps1`은 ECR만 먼저 만들고 clean commit에서 `linux/amd64`용 `service`와 `experiment-aws-s4` image를 build/push한 뒤 tag가 아닌 registry digest를 full plan에 고정합니다. `plan.ps1`은 ECR bootstrap 이후 정확히 67개 create, 0개 update/replace/delete와 NAT/EIP/CloudFront/WAF/autoscaling 부재를 확인합니다. 예상과 다른 기존 state나 drift가 있으면 적용하지 않습니다.

A5 calibration부터 일회성 runner를 다음처럼 시작합니다. 종료한 Task의 결과는 `recovered-results/<deployment-id>/`에도 내려받습니다.

```powershell
.\scripts\run-task.ps1
```

실험을 마쳤거나 어떤 단계에서든 계속하지 않기로 했다면 deadline을 기다리지 않고 정리합니다.

```powershell
.\scripts\destroy.ps1
```

## 로컬 검증

### 본 실험 모드

기본값은 calibration이다. [계측 계약](../../experiments/e1-cache-stampede/AWS-S4-MEASUREMENT.md)의 retained는 bootstrap부터 `-RunMode retained`를 지정한다. Clean public commit의 앞 12자가 deployment ID여야 한다.

```powershell
.\scripts\bootstrap-images.ps1 -DeploymentId $deploymentId -ExpectedCostUsd 3 -RunMode retained
.\scripts\plan.ps1
.\scripts\apply.ps1
.\scripts\resource-diagnostic.ps1
.\scripts\run-task.ps1 -RunMode retained
.\scripts\destroy.ps1
```

각 단계 성공을 확인한 뒤 다음 명령을 실행한다. 실패하면 본 실험을 반복하지 말고 회수 가능한 원자료를 확보한 뒤 destroy한다. 모드 불일치·진단 누락/불일치·기존 로컬/원격 결과·예약 충돌은 차단된다. 실패 run의 예약은 삭제해 재사용하지 않는다. 새 run은 새 checkpoint와 별도 배포에서 검토한다.

### 검증 명령

AWS credential이나 실제 resource 없이 provider schema와 고정 구성을 검증할 수 있습니다.

```powershell
terraform init -backend=false
terraform fmt -check -recursive
terraform validate
terraform test
pwsh -NoProfile -File .\tests\tagged-resources.ps1
pwsh -NoProfile -File .\tests\resource-diagnostic.ps1
pwsh -NoProfile -File .\tests\retained-contract.ps1
pwsh -NoProfile -File .\tests\run-task.ps1
```

`terraform test`는 mock provider로 ECR-only bootstrap과 전체 1/2/4 환경의 plan/apply 계약을 검사합니다. 이는 실제 account의 권한, quota, 생성 가능 여부나 ECS/ALB/S3 runtime 동작을 증명하지 않습니다.

태그 검색에는 삭제된 EC2 자원과 종료된 ECS 기록이 남을 수 있습니다. preflight와 제거 검증은 서비스 API로 EC2 규칙·endpoint의 존재 여부와 ECS 상태를 재확인합니다. 검증된 삭제 기록, `INACTIVE` cluster/service/task definition, `STOPPED` Task는 실행 자원으로 세지 않습니다. 알 수 없는 자원 유형이나 API 조회 실패는 검사를 차단합니다.
