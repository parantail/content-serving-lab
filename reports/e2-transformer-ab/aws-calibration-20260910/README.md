# AWS E2 calibration — 2026-09-10

**AWS 진단·출력 검증·calibration은 성공했지만, 본 측정의 60분 진입 gate를 통과하지 못했다.** Calibration 30.734분에 5회 반복과 25% 여유를 적용하면 192.087분이 필요하다. 5회 본 측정과 AWS quality sweep은 시작하지 않았다. E2의 성능 판정은 미완료이며, 이번 배포의 결과 회수·자원 제거는 완료했다.

| 확인 | 결과 |
| --- | --- |
| 실행 환경 | 서울 Fargate 1.4.0, Linux amd64, Task 할당 1 vCPU·2 GiB, 한 번에 하나의 Task |
| 실행 source | `f60e5b2ee46aa44868e02affba87ac0b5be43995` |
| Runtime image digest | `sha256:18634a03201756b4b377520eab6e237bfeb7b7e684f4c93b3456e7bae1bbf71f` |
| Corpus | 고정 24입력, manifest SHA-256 `127390f507c0d3918b2097eb081511b2952715cc5ac1fbe17683c7745cf5a458` |
| 진단 | 16/16 native 호출 성공, AOM 3.12.1·speed 5 |
| 출력 검증 | 576/576 성공, 독립 디코딩·geometry·alpha·JPEG 4:4:4·PNG truecolor 확인 |
| Calibration | 48/48 batch, 성공 1,152회 + warm-up 1,152회 |
| 추가 사전 진단 | validate/calibrate 각각 같은 Task에서 16회 성공 |
| 전체 완료 호출 | 2,928회; 오류·timeout·interrupted 0 |
| Calibration loop wall | 1,844.035초 = 30.734분 |
| 5회 예상 | 153.670분 |
| 25% 여유 적용 | 192.087분 > 60분, 진입 gate 실패 |
| 최장 calibration native 호출 | 22.790초; warm-up 포함, 사전 진단 제외 |
| Calibration worker RSS 최대 | libvips 846.516 MiB / ImageMagick 877.168 MiB |
| Calibration cgroup sampled peak | libvips 833.715 MiB / ImageMagick 852.793 MiB |
| RSS 대조 | 모든 batch에서 worker self HWM와 개별 wait4 HWM 일치 |
| 출력 hash/bytes 대조 | Calibration 측정 1,152개가 독립 디코딩한 AWS validation Q80 출력과 일치 |
| 배포·제거 | ECR bootstrap 1개 + full plan 20개, 변경/삭제 0개; 총 21개 제거, 잔여 검사 0개 |

## 계측과 해석

Calibration 시간은 warm-up·측정·worker 시작/종료와 batch 사이 업로드를 포함하는 전체 loop다. Worker lifetime wall의 합은 1,836.662초, 그 사이 시간은 7.373초다. 최초 parent corpus/package/manifest 준비와 같은 Task의 사전 AVIF 진단은 loop 앞에 있으며 Task 총시간에는 포함한다. 업로드와 분석은 개별 native 변환의 timing에 포함하지 않는다.

세 Task 모두 실제 AVIF encoder threads는 libvips 1 / ImageMagick 2였다. 표준 Debian 패키지의 delegate 기본 설정 차이를 허용한 배포 후보 비교이며, 동일 encoder thread 비교로 표현하지 않는다. 성능 worker에는 probe를 넣지 않았다. 버전·설정·작업 순서·seed는 각 manifest와 `preflight/settings.json`, 진단 stderr에 보존했다.

Cgroup v1에서 parent+worker 직접 멤버 2개, hidden member 0과 membership 대응을 확인했다. Memory limit는 2,147,483,648 bytes다. 컨테이너에 보이는 CPU quota는 `-1`이었다. 1 vCPU는 ECS Task의 할당값이며 이 관측만으로 상위 CPU 제한을 재구성할 수 없다. Worker RSS와 parent·worker·file cache를 포함하는 cgroup sampled peak는 다른 지표다. 전체 worker thread 표본도 AVIF encoder threads 설정값과 구분한다.

이 자료는 한 번의 calibration이며 로컬 자료와 합치거나 5회 본 측정으로 승격하지 않는다. Q80 검증의 품질 수치는 보존하지만, 미실행 quality sweep을 대체하지 않는다. 처리량–RSS·품질–bytes·입력별 비교의 최종 세 차트와 변환기 선택은 보류한다. 현재 서비스의 libvips 선택도 이번 자료로 새롭게 정당화하지 않는다.

## 원자료와 검증

- [집계와 시간 gate](summary.json), [실행 metadata](raw/execution.json), [회수 원본 SHA-256](raw/sha256.json).
- [독립 출력 검증](analysis/validate/verification.json), [Q80 품질 값](analysis/validate/quality.json), [확대 crop](analysis/validate/crops.png).
- [Calibration 분석](analysis/calibrate/verification.json), [Q80 hash 대조](calibration-hash-check.json), [독립 재생성 대조](reanalysis-check.json).
- [검토한 plan](raw/reviewed-plan.json), [실제 배포 확인](raw/live-verification.json), [제거 기록](raw/cleanup.json), [서비스별 잔여 검사](raw/residual-check.json).
- [사용량·비용 관측](cost-observation.json). 배포 전 보수적 견적은 US$1.00, 승인 비용 조건은 US$3 이내였다. 계정 청구 집계와 자원 사용량으로 계산한 추정치는 구분한다.

원자료는 회수한 bytes 그대로이며 AWS 계정·ARN·네트워크 attachment 등은 별도 metadata에서 제외했다. EXIF 6/8 방향 패턴, 과일 질감과 alpha 경계를 확대 crop으로 확인했다.

## 재생성

공개 저장소 root, Git LFS 파일 확보 후 [tools 이미지](../../../experiments/e2-transformer-ab/Dockerfile.tools)를 build한다. 모든 출력 경로는 새 이름을 쓴다.

```powershell
docker build -f experiments/e2-transformer-ab/Dockerfile.tools -t e2-tools:local .
$evidence = 'reports/e2-transformer-ab/aws-calibration-20260910'
foreach ($mode in @('diagnose', 'validate', 'calibrate')) {
    docker run --rm --network none --cpus=2 --memory=2g --mount "type=bind,source=$((Get-Location).Path),target=/repo" e2-tools:local experiments/e2-transformer-ab/analyze.py "$evidence/raw/aws-$mode-20260910-a1" "dist/e2-reproduce-$mode"
    if ($LASTEXITCODE -ne 0) { throw 'Independent analysis failed.' }
}
python experiments/e2-transformer-ab/checkpoint.py "$evidence/raw" dist/e2-reproduce-summary.json
python experiments/e2-transformer-ab/check_outputs.py dist/e2-reproduce-validate dist/e2-reproduce-calibrate dist/e2-reproduce-hashes.json
```

`summary.json`은 `checkpoint.py`로 원자료의 성공 수·warm-up 수·loop 시간·RSS와 gate를 다시 계산한 값이다. 분석 산출물은 별도 실행의 SHA-256과 대조했다. AWS를 다시 실행하는 명령과 현재 시간 제한은 [배포 문서](../../../deploy/e2-transformer-ab/README.md), [실험 계약](../../../experiments/e2-transformer-ab/README.md)에 있다. 새 시간 제한을 확정하기 전에는 본 측정을 실행하지 않는다.
