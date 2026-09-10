# E2 배포 전 비용 계산

조회일: 2026-09-10. 서울 Linux/x86 Fargate, 한 번에 1 vCPU·2 GiB Task 하나, bootstrap부터 최대 2시간이다. **보수적 실행 견적 US$1.00**, 합의한 US$3 이내다. 자동 과금 차단 한도가 아니며 PC와 인증이 유효해야 로컬 watchdog가 동작한다.

| 항목 | 적용 수량·단가 | 계산 US$ |
| --- | --- | ---: |
| Fargate CPU | 2 vCPU-hours × 0.04656 | 0.09312 |
| Fargate memory | 4 GB-hours × 0.00511 | 0.02044 |
| Public IPv4 | 2 address-hours × 0.005 | 0.01000 |
| ECR 보관 | 1 GB를 한 달 보관하는 금액까지 여유 반영 × 0.10 | 0.10000 |
| S3 보관 | 1 GB를 한 달 보관하는 금액까지 여유 반영 × 0.025 | 0.02500 |
| S3 PUT/LIST | 20,000 × 0.0000045 | 0.09000 |
| S3 GET/기타 | 20,000 × 0.00000035 | 0.00700 |
| Logs 수집 | 0.1 GB × 0.76 | 0.07600 |
| Logs 보관 | 0.1 GB-month × 0.0314 | 0.00314 |
| 외부 결과 회수 전송 여유 | 1 GB에 US$0.20 예산 배정; 실제 단가라는 뜻이 아님 | 0.20000 |
| 소계 | 무료 사용량·credit·단기 보관 prorating을 공제하지 않음 | 0.62470 |
| 추가 여유 | 약 60% | 0.37530 |
| 실행 견적 | 세금·환율 변환 전 USD | **1.00000** |

이미지/회수 데이터가 각각 1 GB를 초과하거나 새 배포·Task 동시 실행을 추가해야 하면 이 계산을 다시 검토한다. 결과의 반복 다운로드도 위 전송량에 포함한다. 배포 후 실제 compressed image 크기·S3 크기·Task 시작/종료 시각을 확인한다.

단가 근거는 AWS 공개 Price List의 서울 항목이다. Fargate 가격 파일의 publication은 2026-08-31, 적용일은 2026-07-01이었다.

- [Fargate 서울 Price List](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonECS/current/ap-northeast-2/index.json): `APN2-Fargate-vCPU-Hours:perCPU`, `APN2-Fargate-GB-Hours`.
- [S3 서울 Price List](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonS3/current/ap-northeast-2/index.json): Standard 첫 storage tier, Requests-Tier1/2.
- [CloudWatch 서울 Price List](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonCloudWatch/current/ap-northeast-2/index.json): DataProcessing-Bytes, TimedStorage-ByteHrs.
- [VPC 서울 Price List](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonVPC/current/ap-northeast-2/index.json): PublicIPv4:InUseAddress.
- [ECR 요금](https://aws.amazon.com/ecr/pricing/): private image storage US$0.10/GB-month.
- [Fargate 과금 설명](https://aws.amazon.com/fargate/pricing/): image 다운로드부터 종료까지 과금한다. 기본 20 GB ephemeral storage를 사용한다.

예상 사용량 계산과 계정 청구 내역은 구분한다. 실행 후 비용 조회 시각·범위·집계 지연을 별도 기록하며, 아직 집계되지 않은 비용을 US$0으로 확정하지 않는다.
