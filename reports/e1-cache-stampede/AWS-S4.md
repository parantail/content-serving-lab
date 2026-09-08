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

이 결과만으로 Redis/DynamoDB 조정을 도입하지 않는다. 실제 service SLO·cold burst 빈도·조정 지연과 장애 복구 비용이 없으므로 S5 도입 판단은 보류한다. [cost.json](../../experiments/e1-cache-stampede/results-aws-s4/retained-ad80a58dee07/cost.json)은 측정 사용량만 담는다. 청구 확정액·월 비용 모델·외부 조정 손익분기는 아직 계산하지 않았다. US$3는 배포 전 예상 상한이지 실제 청구 금액이 아니다.

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
