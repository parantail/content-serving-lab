# E2 배포 전 비용 계산

조회일: 2026-09-10. 서울 Linux/x86 Fargate, 한 번에 1 vCPU·2 GiB Task 하나, bootstrap부터 최대 5시간이다. **보수적 실행 견적 US$1.50**, 합의한 US$3 이내다. 자동 과금 차단 한도가 아니며 PC와 인증이 유효해야 로컬 watchdog가 동작한다.

| 항목 | 적용 수량·단가 | 계산 US$ |
| --- | --- | ---: |
| Fargate CPU | 5 vCPU-hours × 0.04656 | 0.23280 |
| Fargate memory | 10 GB-hours × 0.00511 | 0.05110 |
| Public IPv4 | 5 address-hours × 0.005 | 0.02500 |
| ECR 보관 | 1 GB를 한 달 보관하는 금액까지 여유 반영 × 0.10 | 0.10000 |
| S3 보관 | 1 GB를 한 달 보관하는 금액까지 여유 반영 × 0.025 | 0.02500 |
| S3 PUT/LIST | 20,000 × 0.0000045 | 0.09000 |
| S3 GET/기타 | 20,000 × 0.00000035 | 0.00700 |
| Logs 수집 | 0.1 GB × 0.76 | 0.07600 |
| Logs 보관 | 0.1 GB-month × 0.0314 | 0.00314 |
| 외부 결과 회수 전송 여유 | 1 GB에 US$0.20 예산 배정; 실제 단가라는 뜻이 아님 | 0.20000 |
| 소계 | 무료 사용량·credit·단기 보관 prorating을 공제하지 않음 | 0.81004 |
| 추가 여유 | 약 85% | 0.68996 |
| 실행 견적 | 세금·환율 변환 전 USD | **1.50000** |

이미지/회수 데이터가 각각 1 GB를 초과하거나 새 배포·Task 동시 실행을 추가해야 하면 이 계산을 다시 검토한다. 결과의 반복 다운로드도 위 전송량에 포함한다. 배포 후 실제 compressed image 크기·S3 크기·Task 시작/종료 시각을 확인한다.

단가 근거는 AWS 공개 Price List의 서울 항목이다. Fargate 가격 파일의 publication은 2026-08-31, 적용일은 2026-07-01이었다.

- [Fargate 서울 Price List](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonECS/current/ap-northeast-2/index.json): `APN2-Fargate-vCPU-Hours:perCPU`, `APN2-Fargate-GB-Hours`.
- [S3 서울 Price List](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonS3/current/ap-northeast-2/index.json): Standard 첫 storage tier, Requests-Tier1/2.
- [CloudWatch 서울 Price List](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonCloudWatch/current/ap-northeast-2/index.json): DataProcessing-Bytes, TimedStorage-ByteHrs.
- [VPC 서울 Price List](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonVPC/current/ap-northeast-2/index.json): PublicIPv4:InUseAddress.
- [ECR 요금](https://aws.amazon.com/ecr/pricing/): private image storage US$0.10/GB-month.
- [Fargate 과금 설명](https://aws.amazon.com/fargate/pricing/): image 다운로드부터 종료까지 과금한다. 기본 20 GB ephemeral storage를 사용한다.

예상 사용량 계산과 계정 청구 내역은 구분한다. 실행 후 비용 조회 시각·범위·집계 지연을 별도 기록하며, 아직 집계되지 않은 비용을 US$0으로 확정하지 않는다.

첫 배포 `20260910-a1`은 진단·검증·calibration 후 본 측정 시간 gate에서 중단하고 21개 자원을 제거했다. [사용량·비용 관측](../../reports/e2-transformer-ab/aws-calibration-20260910/cost-observation.json)에 Task 시각, image 168,652,751 bytes, S3 896개·76,143,490 bytes와 당일 Cost Explorer 조회를 기록했다.

후속 `20260910-b1`은 다섯 mode 실행 후 quality 전달 오류를 확인했고 2026-09-10 19:46 KST에 21개 자원을 모두 제거했다. [후속 사용량·비용 관측](../../reports/e2-transformer-ab/aws-20260910/cost-observation.json)의 image는 168,653,619 bytes, S3는 2,166개·103,515,646 bytes다. Task 시각과 위 단가로 계산한 CPU·memory 소계는 US$0.194960이며 기타 자원 비용을 제외한 추정치다. 19:47 KST의 account-wide 당일 Usage 집계는 비어 있고 `Estimated=true`였으므로 E2 비용 US$0, 청구 확정액 또는 전체 비용으로 해석하지 않는다.

수정본 `20260910-c2`는 다섯 mode 성공 후 2026-09-11 03:37 KST에 21개 자원 제거·잔여 0개를 확인했다. [사용량·비용 관측](../../reports/e2-transformer-ab/aws-20260910-c2/cost-observation.json)의 image는 168,654,878 bytes, S3 결과는 2,166개·106,731,634 bytes다. Task pull~정지 시각 13,038초와 위 단가의 CPU·memory 추정 소계는 US$0.205638이다. 03:33 KST에 조회한 UTC 09-10 계정 전체 Usage는 US$0.0559455727·Estimated=true였다. 집계 지연과 다른 실행이 섞인 값이며 c2 귀속 비용이나 확정 invoice로 해석하지 않는다. 사전 US$1.50 견적과 US$3 실행 조건을 유지했다.
