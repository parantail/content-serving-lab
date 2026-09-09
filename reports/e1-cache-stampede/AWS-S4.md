# E1 AWS S4: Task 수와 동일 이미지 중복 변환

2026-09-08의 단일 retained 실행에서 **100개 cold 요청을 받은 Task가 1·2·4개일 때 변환은 각각 1·2·4회 발생했다.** S3에는 trial마다 완성된 결과 하나가 생성됐고 나머지 조건부 PUT은 기존 결과를 사용했다. 30개 trial과 3,000개 요청 모두 검증을 통과했다. 분산 조정은 사용하지 않았다.

## 조건과 원자료

- 실행 commit: `ad80a58dee07cc0ba0e497d57949afe11c7e43be`, run ID `retained-ad80a58dee07`, 계약 `e1-aws-s4-retained-v1`.
- Seoul 단일 Region, 내부 ALB, Fargate Media 서비스 1/2/4 Task 각각 1 vCPU·2GiB, 별도 runner 1 vCPU·2GiB. 세 서비스는 동시에 유지하고 1→2→4·4→2→1 순서를 교차했다.
- 각 scenario 10회, 매 trial 파생 object 삭제·cold 확인 후 같은 100개 요청을 하나의 barrier로 해제했다. 입력은 고정 landscape fixture, 640×640 cover/quality80 WebP다.
- Sampling 50ms, 시작 시각 차이 상한 50ms, 요청/제어 timeout 90초/30초, control polling 100ms. 실제 start-skew 최대는 7.090339ms였다.
- Media digest: `sha256:8d040bec83be619c647ea9b645eff965bceaae6100407cdaa719af050ba74ef8`.
- Runner digest: `sha256:8e6b19480937ecc0f730c0e46d3a65e63bfa9fbe4a2e08ffb23faeb0ef18ea0c`.
- [전체 원자료·분석](../../experiments/e1-cache-stampede/results-aws-s4/retained-ad80a58dee07/), [trial별 요약](../../experiments/e1-cache-stampede/results-aws-s4/retained-ad80a58dee07/analysis/summary.csv), [동일-image 진단](aws-s4-retained-diagnostic/diagnostic.jsonl), [진단 실행 환경](aws-s4-retained-diagnostic/execution.json).

## 결과

CPU는 trial에서 참여 Task들의 관측 CPU 시간 합계다. 아래 평균·중앙값은 **10개 trial의 통계**이며 전체 1,000개 요청을 합친 p99가 아니다.

| Task 수 | trial당 변환/Original GET/조건부 PUT | CPU 합계 평균 ms | trial p99 평균 ms | trial p99 중앙값 ms | trial p99 범위 ms |
| --- | --- | ---: | ---: | ---: | --- |
| 1 | 1 / 1 / 1 | 1,076.76 | 1,121.98 | 1,097.45 | 1,059.47–1,253.99 |
| 2 | 2 / 2 / 2 | 1,955.26 | 1,141.41 | 1,075.04 | 1,000.46–1,777.27 |
| 4 | 4 / 4 / 4 | 3,578.62 | 1,021.43 | 1,015.56 | 966.24–1,111.57 |

- CPU 합계 평균은 1 Task 대비 약 1.82배/3.32배다. Task별 병렬 CPU 자원이 늘어난 비교이므로 동일 CPU 예산의 효율 실험으로 해석하지 않는다.
- Trial p99 평균 차이는 2 Task에서 +19.44ms(+1.73%), 4 Task에서 -100.55ms(-8.96%)다. 2 Task의 10번째 trial p99=1,777.27ms도 포함했다. 단일 실행·각 10회·합성 workload이므로 성능 개선의 일반적 보장이나 인과 추정이 아니다.
- 3,000/3,000 HTTP 200, response hash 1종, 변환 성공 70회, created/existing 30/40회, conflict·publish 오류·timeout·5xx 없음. Task별 요청은 1 Task=100개, 2 Task=49–51개, 4 Task=24–27개였다.
- CPU zero Task 행 0/70, resource error 0, 전 Task source는 `cgroup-v1-container-visible`, 실제 sampler 설정은 50ms다.

## 계측 범위와 변동

동일-image 진단은 Fargate 1.4.0에서 CPU/memory mount·membership 대응, self 직접 멤버 1개·숨겨진 멤버/하위 group 0개를 확인했다. CPU 부하에서 cgroup/process CPU 차이는 최대 약 0.234ms였다. 무부하 sampling 추가 CPU는 반복별 약 7.921/6.583/8.055ms/약 1초다. Busy 한 쌍은 off 약 993ms, on 약 908ms로 실행량 변동이 컸고 다른 쌍은 반대 방향이었다. 이를 전부 보존했으며 그 차이를 순수 sampler 비용으로 간주하지 않는다.

CPU에는 prepare~finish 사이의 sampler·runtime·제어·저장소 작업이 포함되며 순수 libvips CPU나 청구 vCPU 시간이 아니다. Task들의 측정 경계도 완전히 같지 않다. 무부하 값을 차감하지 않았다.

Memory는 Task별 sampled peak 중 최댓값이며 Task 전체 합계나 정확한 순간 peak가 아니다. 실행 전체의 이 값 최댓값은 1/2/4 Task에서 273.76/284.77/322.11MiB였다. 반복 간 allocator/cache 잔류를 포함하며 증가분 메모리로 해석하지 않는다.

Resource sample 1,955개, 인접 간격 1,885개를 관측했다. Timestamp 간격 중앙값 50.2899ms, nearest-lower p99 62.8417ms, 최대 100.8238ms다. 50ms는 목표 간격이지 보장이 아니다. 원자료 timestamp를 100ns 정밀도로 읽어 계산했으며 간격을 보간하거나 sample을 제거하지 않았다.

## 판단과 미완료 범위

프로세스 내부 요청 합치기는 각 Task 안에서 작동했지만 Task 간 중복 변환을 제거하지 못했다. S3 조건부 저장은 완성된 object 충돌을 해결했으며 중복 계산 자체를 막지는 않았다.

서버 간 분산 조정(S5)은 이번 E1에서 진행하지 않는다. 현재 결과만으로 추가 시스템의 필요성이 충분히 입증되지 않았고, 잠금 만료·장애 복구·추가 지연 검증은 실험 범위를 크게 확대한다. 프로세스 내부 요청 합치기와 S3 조건부 저장의 효과·한계를 확인하는 범위로 기술 실험을 마친다. 실제 service SLO·cold burst 빈도·외부 조정 비교 측정이 없으므로 분산 조정의 일반적 불필요성이나 비용 열위를 주장하지 않는다. [cost.json](../../experiments/e1-cache-stampede/results-aws-s4/retained-ad80a58dee07/cost.json)은 측정 사용량만 담는다. 별도 Cost Explorer CSV의 E1 전체 Usage 합계는 US$0.6776208602이며 Bills 표시 US$0.68과 대조했다. 아래 실행 이력·비용 절을 참고한다. S5 도입을 위한 월 비용 모델·외부 조정 손익분기는 이번 산출물에서 제외하며 계산 완료로 간주하지 않는다. US$3는 배포 전 예상 상한이지 실제 청구 금액이 아니다.

## 전체 AWS 실행 이력과 비용

2026-09-09 확인한 E1 전용 계정의 전체 AWS Usage 비용은 **US$0.6776208602(약 US$0.68)**다. 단일 본 측정의 비용이 아니라 초기 실패·개발 calibration·계측 진단·본 측정과 배포 유지 시간을 포함한 E1 전체 AWS 사용료로 기록한다. 계정에 E1 외 사용이 없다는 운영자 확인을 집계 범위의 근거로 삼았다.

### 실행 횟수의 기준

| 배포 식별자 | 실행 내용 | 확인한 결과 |
| --- | --- | --- |
| `645bfa12ab2d` | 초기 배포 복구 후 개발 부하 실행 | 첫 CONTROL 분석 실패, 원자료 미회수. 당시 실행 기록으로만 확인 |
| `ef0886d80ca2` | 개발 부하 시험 (calibration) | 30 trial 기록. CPU 0 문제로 자원 성능 근거에서는 제외 |
| `32d01d832c45` | 직접 cgroup 계측으로 개발 부하 재시험 | 30 trial 기록, CPU 0 문제 해소 |
| `43942a825e47` | Fargate 계측 진단 | 12구간·범위 4행·완료 1행, CPU mount 대응 미확인 |
| `0d0beb9949b1` | 별칭 수정 후 Fargate 계측 재진단 | 12구간·범위 4행·완료 1행, mount 대응 확인 |
| `ad80a58dee07` | 동일 이미지 진단 후 본 측정 | 진단 17행과 retained 30 trial 완료 |

따라서 확인되는 배포 묶음은 **6개**, 부하 실행은 **4회(실패 1회 + 원자료가 보존된 3회)**, 계측 진단은 **3회**다. 마지막 배포는 진단과 본 측정을 함께 포함하므로 이 횟수들을 배포 횟수로 더하지 않는다. 배포 복구·CLI 재시도는 별도 배포 묶음으로 세지 않았다. 로컬 Docker 실험은 AWS 실행 횟수에서 제외한다.

보존된 부하 run은 `calibration-ef0886d80ca2`, `calibration-32d01d832c45`, `retained-ad80a58dee07`이며 각 `run.json`과 `analysis/analysis.json`에서 30 trial씩 확인했다. 총 90 trial 중 최종 성능 근거는 retained 30개뿐이다. 진단 3회는 각 `execution.json`과 17행 JSONL로 확인했다. 초기 실패는 원자료가 없어 결과 수치나 완료 trial 수를 복원하지 않는다. 초기 calibration 원자료는 로컬 회수본이며 공개 retained 원자료와 혼합하지 않았다.

### 서비스별 Usage 비용

[Cost Explorer 원본 CSV](aws-s4-costs.csv)는 `Charge type: Usage`로 조회한 서비스별 금액이다. 날짜 행은 `2026-09-08` 하나이며 `Service total`과 동일하므로 둘을 합산하지 않는다.

| 서비스 | USD |
| --- | ---: |
| Elastic Container Service | 0.4802010774 |
| Elastic Load Balancing | 0.1416824209 |
| VPC | 0.0483958750 |
| S3 | 0.0072942560 |
| EC2 Container Registry (ECR) | 0.0000422309 |
| Secrets Manager | 0.0000050000 |
| Glue / Key Management Service / CloudWatch | 0 |
| **합계** | **0.6776208602** |

서비스 합계와 CSV 총액은 일치한다. 운영자가 확인한 Bills 표시와 크레딧 사용 표시는 각각 US$0.68로, 반올림한 Usage 합계와 일치한다. CSV에는 Credit 행이 없으므로 정확한 크레딧 상계액이나 순지불액을 이 파일에서 직접 산출하지 않는다.

이 금액은 조회 시점에 반영된 사용료이며 월말 확정 invoice가 아니다. CSV에는 사용량·단가·배포별 식별자가 없으므로 서비스 사용 시간·요청 수나 각 실행 비용으로 역산·균등 배분하지 않는다. CSV의 0도 해당 조회에서 비용이 0이라는 뜻이며 사용량 0의 증거는 아니다. 원자료 `cost.json`은 측정 사용량으로 그대로 보존하며 계정 전체 금액을 단일 run에 주입하지 않는다.

CSV SHA-256: `422546218d2dc0821ea93b8293165e8153af5b883a6cd8f4cb5cfabacd31e15d`.

## 재검증

[실행 절차](../../deploy/e1-aws-s4/README.md)와 [계측 계약](../../experiments/e1-cache-stampede/AWS-S4-MEASUREMENT.md)을 따른다. 로컬 재분석은 공개 저장소 root에서 수행할 수 있다. 원자료를 보존하려면 별도 사본에 실행한다.

```powershell
docker build --target experiment-aws-s4 --build-arg GIT_COMMIT=ad80a58dee07cc0ba0e497d57949afe11c7e43be -t e1-aws-s4-analysis .
docker run --rm --network none --mount "type=bind,source=$((Get-Location).Path),target=/repo,readonly" --entrypoint sh e1-aws-s4-analysis -c 'cp -R /repo/experiments/e1-cache-stampede/results-aws-s4/retained-ad80a58dee07 /tmp/recheck && /app/e1-aws-s4 analyze --run-dir /tmp/recheck'
```

측정 당시 runner 이미지에서도 network none·입력 read-only로 독립 재분석하여 30 valid/0 invalid를 재현했고 분석 파일 전체가 회수본과 바이트 단위로 일치했다.

원자료 SHA-256:

- `run.json`: `03dc3201c52567e458b5df180a2468e785da2a45c1b3b2c9750a2261adb6f7cb`
- `tasks.csv`: `f8d53ac3167eb2951b53404cb1dd7042a83871f5f9b20a1e34faa671448aef3d`
- `resources.csv`: `61da25cccd607b19da79ffa04ec88a9ce904adf09b3ae24d873637e4318da870`
- `requests.csv`: `3aca2a6a57e1852fb7f7f686c9fabe43b3b94ee69998fcabf7b17d7e57cf073e`

실험 후 Terraform 71개 삭제, state empty와 서비스 API 기반 독립 잔여 실행 자원 검사 통과를 확인했다. 원자료를 로컬에 회수한 후 실험 bucket·image repository·log group을 포함한 일회성 AWS 자원을 제거했다. Raw와 분석은 위 공개 경로에 보존한다.
