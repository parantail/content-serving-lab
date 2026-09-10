# E2 one-shot Fargate

서울의 Linux amd64 Task 하나에서 E2 library worker를 교대로 호출한다. ECS Service·ALB·NAT 없이 1 vCPU·2 GiB를 고정한다. [실험 계약](../../experiments/e2-transformer-ab/README.md), [비용 계산](COST.md)을 먼저 확인한다.

2026-09-10 `20260910-b1`에서 210분·인프라 5시간 계약으로 다섯 mode를 모두 실행하고 21개 자원을 제거했다. [실행·회수·제거 기록](../../reports/e2-transformer-ab/aws-20260910/README.md)에 원자료와 독립 잔여 0개 검사를 보존했다. ImageMagick AVIF quality 전달 오류를 수정하고 실제 encoder Q80 gate를 추가했다. AWS 재인증 후 수정본의 전체 local validation/calibration/quality 3,120회와 시간 gate를 통과했다. 이 clean checkpoint로 AWS matrix를 다시 실행하며 AWS calibration을 별도로 확인한다. [수정본 로컬 기록](../../reports/e2-transformer-ab/local-20260910-c2/README.md). 과거 결과는 최종 비교 근거로 사용하지 않는다. 이전 `20260910-a1`의 [60분 gate 중단 기록](../../reports/e2-transformer-ab/aws-calibration-20260910/README.md)은 별도로 보존한다.

아래 명령은 공개 저장소 root의 PowerShell 7에서 실행한다. AWS CLI 2.32+, Terraform 1.15–1.16, Docker와 유효한 sandbox `aws login` 세션이 필요하다. E1의 checked CLI·자격/예산 검사·로컬 watchdog 패턴을 재사용하며 E1 state는 변경하지 않는다.

```powershell
terraform -chdir=deploy/e2-transformer-ab init -backend=false -lockfile=readonly
terraform -chdir=deploy/e2-transformer-ab validate
terraform -chdir=deploy/e2-transformer-ab test
# 독립 검증·calibration이 끝난 clean commit에서 실행한다.
./deploy/e2-transformer-ab/scripts/bootstrap.ps1 -DeploymentId <unique-id> -ExpectedCostUsd 1.50
./deploy/e2-transformer-ab/scripts/plan.ps1
# local/plan-review.json과 terraform show를 검토하고 저장된 plan SHA256을 사용한다.
./deploy/e2-transformer-ab/scripts/apply.ps1 -ReviewedPlanSHA256 <sha256>
./deploy/e2-transformer-ab/scripts/run.ps1 -Mode diagnose -RunId <unique-diagnostic-id>
./deploy/e2-transformer-ab/scripts/run.ps1 -Mode validate -RunId <unique-validation-id>
./deploy/e2-transformer-ab/scripts/run.ps1 -Mode calibrate -RunId <unique-calibration-id>
# 동일 이미지 진단·출력 검증·calibration gate를 확인한 뒤 실행한다.
./deploy/e2-transformer-ab/scripts/run.ps1 -Mode measure -RunId <unique-measurement-id>
./deploy/e2-transformer-ab/scripts/run.ps1 -Mode quality -RunId <unique-quality-id>
./deploy/e2-transformer-ab/scripts/destroy.ps1
```

Bootstrap는 실제 생성 전 5시간 deadline와 숨겨진 PowerShell watchdog를 시작한다. ECR 하나만 생성하는 저장된 bootstrap plan을 검사하고 immutable commit tag를 push한다. 전체 plan은 `image@sha256`를 사용한다. 배포/Task 상세·plan·회수 자료는 Git 제외 `local/`에 보관한다. Terraform state, runtime 설정과 watchdog 기록도 Git/Docker context에서 제외한다.

결과는 supervisor가 각 batch 종료 후 조건부 S3 PutObject로 업로드한다. 같은 run prefix의 원자료를 덮어쓰지 않는다. 네트워크와 업로드는 transform/batch 측정 구간 밖이다. container stdout에는 진행 batch만 출력하며 native 상세/원자료는 S3로 회수한다. Task 실행 실패 시 후속 run을 중단하고 가용 결과를 먼저 회수한다.

각 Task의 batch loop 앞에 별도 AVIF probe worker를 실행해 해당 호스트의 설정을 기록한다. 처음 diagnostic Task에서 읽은 thread 값을 다른 Task에도 같다고 가정하지 않는다. 실제 측정 worker에는 probe를 주입하지 않는다.

`destroy.ps1`은 전용 cluster의 Task를 종료하고 S3 결과를 회수한 후 Terraform destroy를 실행한다. 회수 오류가 있어도 deadline 정리는 계속하며 오류를 기록한다. State 외에 EC2 network·S3·ECR·IAM·Logs·ECS API의 잔여 수를 확인한다. 종료한 Task 이력과 inactive task definition은 실행 중 자원이 아니며 AWS 보존 이력으로 남을 수 있다. 예산·sandbox IAM 사용자 등 기존 계정 설정은 제거 대상이 아니다.

watchdog는 PC와 AWS 인증에 의존한다. 이를 AWS 자체 예약 정리로 표현하지 않는다. 인증 문제로 cleanup이 실패하면 즉시 사용자 수동 대응이 필요하다. 성공적으로 제거한 뒤에도 runtime/deadline/원자료를 보존하며, 다음 배포는 이 기록을 명시적으로 archive한 후 시작한다.

본 측정 Task 외부 감독은 시작 요청부터 215분이며, 실행 직전에 인프라 deadline까지 240분 이상 남아 있어야 한다. 이는 loop 210분·Task 준비/종료 5분·quality 15분·cleanup 10분의 여유다. 다른 mode의 Task 감독은 63분이며 모든 mode에 인프라 deadline을 우선 적용한다. 시간 여유는 완료 보장이 아니며 실행 오류나 gate 실패 시 후속 run을 중단한다.
