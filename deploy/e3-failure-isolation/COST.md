# E3 배포 전 비용 계산

조회일: 2026-09-11. 서울 Linux/x86 Fargate, 한 번에 1 vCPU·2 GiB Task 하나, bootstrap부터 최대 2시간이다. **보수적 실행 견적 US$1.20**, 사전에 정한 상한 US$3 이내다. 자동 과금 차단 한도가 아니며 PC와 인증이 유효해야 로컬 watchdog가 동작한다. 단가는 [E2 비용 계산](../e2-transformer-ab/COST.md)과 같은 서울 Price List 항목을 사용한다.

| 항목 | 적용 수량·단가 | 계산 US$ |
| --- | --- | ---: |
| Fargate CPU | 2 vCPU-hours × 0.04656 | 0.09312 |
| Fargate memory | 4 GB-hours × 0.00511 | 0.02044 |
| Public IPv4 | 2 address-hours × 0.005 | 0.01000 |
| ECR 보관 | 1 GB를 한 달 보관하는 금액까지 여유 반영 × 0.10 | 0.10000 |
| S3 보관 | 1 GB를 한 달 보관하는 금액까지 여유 반영 × 0.025 | 0.02500 |
| S3 PUT/LIST | 20,000 × 0.0000045 (파생 저장 약 7,000개, 결과 업로드, 예열 포함 여유) | 0.09000 |
| S3 GET/기타 | 150,000 × 0.00000035 (hit 20 req/s × 150초 × 29 trial 등) | 0.05250 |
| Logs 수집 | 0.1 GB × 0.76 | 0.07600 |
| Logs 보관 | 0.1 GB-month × 0.0314 | 0.00314 |
| 외부 결과 회수 전송 여유 | 1 GB에 US$0.20 예산 배정; 실제 단가라는 뜻이 아님 | 0.20000 |
| 소계 | 무료 사용량·credit·단기 보관 prorating을 공제하지 않음 | 0.67020 |
| 추가 여유 | 약 79% | 0.52980 |
| 실행 견적 | 세금·환율 변환 전 USD | **1.20000** |

AWS 측정 범위는 2026-09-11에 정한 대로 T0·T2·T4 × M0/M1/M2 × 3회의 27 trial과 calibration 2 trial이다. 각 trial은 150초 timeline과 예열·drain을 포함해 약 2.75분이며, calibration 약 6분·본 측정 약 75분·image build/push·plan/apply·destroy를 합쳐 2시간 deadline 안에 끝내는 것을 전제로 한다. 시간이 부족하면 본 측정을 시작하지 않는다.

예상 사용량 계산과 계정 청구 내역은 구분한다. 실행 후 비용 조회 시각·범위·집계 지연을 별도 기록하며, 아직 집계되지 않은 비용을 US$0으로 확정하지 않는다.

## 실행 후 관측

Deployment `e8c21674a86f`는 2026-09-11 12:49 UTC bootstrap부터 13:59 UTC destroy까지 약 70분 동안 calibration Task 2개(각 약 6분)와 measure Task 1개(45.6분)를 실행했다. Task 시각과 위 단가로 계산한 measure Task의 CPU·memory 소계는 US$0.043이다.

다음 날(2026-09-12) 운영자가 내려받은 Cost Explorer 일별 서비스 CSV의 2026-09-11(UTC) 행은 ECS US$0.0532, S3 US$0.0172, VPC(public IPv4) US$0.0048, ECR US$0.00001, Cost Explorer API US$0.01, 합계 **US$0.0851**이다. E2의 마지막 실행은 2026-09-10 18:37 UTC에 끝났으므로 이 행은 E3와 비용 조회 API 요금만 담는다. Cost Explorer 항목을 빼면 E3 자원 비용은 약 US$0.075로 사전 견적 US$1.20과 상한 US$3 안이다. 이전 실험에서 일별 행은 약 24시간 안에 안정됐지만 월말 확정 invoice와는 구분한다. [cost-observation.json](../../experiments/e3-failure-isolation/results-aws/cost-observation.json)에 두 관측을 함께 기록했다.
