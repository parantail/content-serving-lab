# AWS E2 — libvips 유지, 처리량 이점과 메모리·품질 상충

**이번 배포 후보 비교에서는 libvips를 유지한다.** 서울 Fargate 1 vCPU·2 GiB에서 Q80 처리량 중앙값은 JPEG·PNG·WebP의 조건별로 ImageMagick의 **2.13~3.17배**, AVIF는 **1.25~1.81배**였다. 그러나 libvips의 RSS가 항상 낮지는 않았고, ImageMagick이 더 작은 파일과 높은 SSIM을 보인 입력도 있었다. Q80 고정 성능을 동등 화질 성능으로 해석하지 않는다.

![처리량과 worker peak RSS, 다섯 반복의 중앙값과 범위](figures/throughput-rss.png)

**E2의 로컬 검증·AWS 다섯 mode·독립 재분석·자원 정리를 완료했다.** AWS native 14,672회 모두 성공, 오류·timeout·interrupted 0회다. 각 AWS Task에서 실제 AVIF Q80을 조회했고 품질별 출력 변화도 확인했다. 2026-09-11 03:37 KST에 자원 21개를 제거했으며 잔여 검사도 모두 0개였다. [최종 검증](assessment.json), [전체 집계](summary.json), [정리 근거](raw/residual-check.json).

## 질문과 사전 선택 기준

같은 입력과 자원 제한에서 Go native binding으로 호출하는 두 변환기의 처리량·RSS·출력 품질·크기를 비교한다. 초기 가설은 libvips thumbnail의 큰 이미지 축소 최적화가 유리할 수 있다는 것이었다. [사전 기준](../../../experiments/e2-transformer-ab/README.md)은 geometry/orientation/alpha와 제한 정책을 먼저 만족하고, 유사 품질의 관측점과 자원·처리량 상충을 검토하는 것이다. 초기 유지 대상은 libvips이며 단일 가중 점수나 사후 품질 문턱으로 승자를 만들지 않는다.

비교 대상은 E2의 최적화된 buffer-thumbnail 경로와 ImageMagick Q16 non-HDRI 경로다. 기존 E1의 image-thumbnail 성능 원자료와 서비스 구현은 그대로다. 따라서 이번 비율을 E1 서비스의 성능 개선율로 사용하지 않는다. [호출 경로와 E1 연결 확인](../../../experiments/e2-transformer-ab/RUN.md).

## 고정 조건과 실행

| 항목 | 실제 조건 |
| --- | --- |
| 날짜·리전 | 2026-09-10~11 KST, 서울 `ap-northeast-2`; AWS 실행 UTC 날짜는 09-10 |
| 환경 | Linux amd64, Fargate 1.4.0, Task 1 vCPU·2 GiB, 한 번에 Task 1개 |
| 실행 source | `eea79eca00ca41632777644ccbb3d2a3eaf534a7` |
| 실행 image | `sha256:51e4736d49e6bbabb3729e22e25dfb91e1b90291f592119870668396ce8171b8` |
| 직접 호출 | Go 1.26.7 → govips 2.16.0 / libvips 8.16.1, imagick 3.7.3 / ImageMagick 7.1.1.43 Q16 non-HDRI |
| AVIF | libheif 1.19.8 / AOM 3.12.1, speed 5, 실제 Q80, encoder threads libvips 1 / ImageMagick 2 |
| 입력 | 24개 fixture·품질 대표 8개, 독립 lossless reference 72개, invalid 3개 |
| Corpus SHA-256 | `127390f507c0d3918b2097eb081511b2952715cc5ac1fbe17683c7745cf5a458` |
| Geometry | 장변 640 resize, 640×480 contain, 중앙 640×640 cover; upscale 없음 |
| Matrix | 세 geometry × JPEG/PNG/WebP/AVIF × 동시성 1/4 × 두 engine × 5회 = 240 batch |
| Warm-up·순서 | 각 성능 batch는 24입력 warm-up 후 24입력 측정, 반복별 engine 순서 교대·seed 209~213 |
| 제한 | Native 30초, 본 측정 loop 210분, 인프라 최대 5시간, 예상 비용 US$3 이내 |

[Corpus](../../../experiments/e2-transformer-ab/fixtures/README.md)는 CC0 사진 2종과 합성 alpha·EXIF 패턴의 크기·포맷 변형이다. 서로 다른 실제 콘텐츠 24종이라는 뜻은 아니다. JPEG 4:4:4, WebP method 4, AVIF 8-bit·Q90 미만 4:2:0/Q90 이상 4:4:4 등의 preset은 [명세](../../../experiments/e2-transformer-ab/RUN.md)에 고정했다. PNG는 compression 6이며 libvips RGB/RGBA, ImageMagick RGBA·filter 0으로 배포 후보 설정을 비교한다.

Worker는 batch마다 새 프로세스지만 이미지 변환마다 Go에서 native library를 직접 호출한다. Native timing은 encoded bytes 반환과 native 객체 해제까지이며 hash·파일 저장·업로드를 제외한다. Batch 처리량에는 scheduling·hash·프로토콜 overhead가 포함된다. HTTP·S3 latency는 측정 대상 밖이다.

| AWS mode | Batch | 측정/출력 성공 | Warm-up 성공 | 같은 Task 별도 진단 | Loop 초 |
| --- | ---: | ---: | ---: | ---: | ---: |
| diagnose | 2 | 16 | 0 | 0 | 28.551 |
| validate | 24 | 576 | 0 | 16 | 474.783 |
| calibrate | 48 | 1,152 | 1,152 | 16 | 2,009.839 |
| measure | 240 | 5,760 | 5,760 | 16 | 10,034.192 |
| quality | 24 | 192 | 0 | 16 | 185.457 |

Calibration 33.497분 × 5 × 1.25 = **209.358분**으로 210분 gate를 통과했고, 별도의 다섯 반복 본 측정은 **167.237분**에 끝났다. Calibration을 다섯 반복에 넣거나 반복을 보충하지 않았다. [진입 gate](raw/calibration-gate.json), [Task 실행 metadata](raw/execution.json). 별도 [수정본 로컬 3,120회 검증](../local-20260910-c2/README.md)의 timing은 AWS와 합치지 않는다.

## 처리량·RSS와 입력별 예외

아래는 cover640, 24입력 batch의 다섯 반복 중앙값이다. 처리량 단위는 성공 변환/초, RSS는 worker 자체 lifetime high-water mark의 MiB다. 전체 48개 engine별 집계·반복 min/max는 [summary](figures/summary.json), 24개 조건 쌍은 [conditions](figures/conditions.json)에 있다.

| 출력 | 동시성 | libvips 처리량 | ImageMagick 처리량 | libvips RSS | ImageMagick RSS |
| --- | ---: | ---: | ---: | ---: | ---: |
| JPEG | 1 | 4.330 | 1.860 | 506.1 | 414.8 |
| JPEG | 4 | 3.487 | 1.323 | 807.2 | 752.7 |
| PNG | 1 | 3.668 | 1.449 | 542.9 | 423.8 |
| PNG | 4 | 2.978 | 1.115 | 832.5 | 766.0 |
| WebP | 1 | 3.608 | 1.697 | 494.9 | 415.2 |
| WebP | 4 | 2.847 | 1.179 | 816.7 | 756.3 |
| AVIF | 1 | 0.684 | 0.420 | 504.3 | 490.6 |
| AVIF | 4 | 0.446 | 0.355 | 739.0 | 869.1 |

24개 조건 쌍 모두 libvips의 처리량 최솟값이 ImageMagick의 최댓값보다 높았다. 이는 이번 다섯 반복의 관측 범위이며 신뢰구간이나 다른 호스트에 대한 보장은 아니다. JPEG·PNG·WebP의 비율은 2.126~3.172, AVIF는 1.254~1.813이었다. 반면 위 cover 조건에서 libvips의 RSS가 낮은 것은 AVIF 동시성 4뿐이다.

같은 engine/geometry/format에서 동시성 4의 처리량 중앙값은 동시성 1의 **0.651~0.872배**였다. 이번 1 vCPU에서는 요청 병렬도를 늘리는 것이 처리량 이득으로 이어지지 않았다. 서비스의 최적 동시성이나 더 큰 CPU 할당 결과까지 확정하지 않는다. [Batch p50/p95와 전체 관측](analysis/measure/batches.json).

![입력·geometry·포맷별 duration과 bytes, 다섯 반복의 중앙값 비](figures/input-matrix.png)

입력별 ImageMagick/libvips 비를 log2로 표시했다. 양수 duration은 libvips가 빠름, 양수 bytes는 libvips가 작음을 뜻한다. 숫자는 실제 log2 값이고 색은 ±2에서 포화된다. 576개 입력별 조건 쌍 중 28개에서는 ImageMagick의 duration 중앙값이 더 짧았다. 예를 들어 `fruit-small-avif` → cover/AVIF 동시성 4는 libvips 11,826.776ms, ImageMagick 7,392.426ms였다. 동시성 4의 개별 호출 시간에는 함께 실행되는 호출의 자원 경합도 들어간다. 이 예외를 batch 처리량과 혼동하지 않으며 RSS를 입력별로 나누지 않는다. [개별 측정값](analysis/measure/transforms.json).

## 품질과 실제 quality 전달

![입력별 SSIM과 bytes, Q50/65/80/90](figures/quality-bytes.png)

576개 validation과 192개 quality 출력을 독립 디코딩해 geometry·orientation·alpha와 preset을 확인했다. 품질은 독립 reference에 대한 encoded-sRGB RGB SSIM(window 7, uniform weight, sample covariance, 채널 평균)·PSNR이다. Alpha는 검정/흰 배경 합성과 alpha plane 오차를 따로 기록하고, 그래프는 두 배경 SSIM의 최솟값을 쓴다. 완전 일치 PSNR은 JSON null/+∞다. [전체 품질 값](analysis/quality/quality.json), [Q80 쌍](figures/quality-q80.json).

| 입력·출력 | libvips 요청 Q / bytes / SSIM | ImageMagick 요청 Q / bytes / SSIM | 해석 |
| --- | --- | --- | --- |
| `fruit-small-png` → AVIF | 80 / 27,214 / 0.969574 | 80 / 27,255 / 0.969809 | 가까운 크기·SSIM의 관측점 |
| `alpha-large-webp` → JPEG | 80 / 26,280 / 0.982032 | 80 / 22,009 / 0.984632 | ImageMagick이 더 작고 SSIM도 높음 |
| `alpha-large-webp` → WebP | 90 / 14,090 / 0.982349 | 80 / 17,046 / 0.982755 | 비슷한 SSIM을 만드는 요청 Q가 다름 |

첫 AVIF 예시의 cover·동시성 1 호출 중앙값은 libvips 1,292.793ms, ImageMagick 1,983.483ms였다. 두 번째 JPEG 예시는 166.196ms 대 1,444.710ms로 파일 크기·SSIM 이득과 처리 시간의 상충을 보여준다. 세 번째의 libvips Q90에는 다섯 반복 성능 측정이 없으므로 이 점을 Q80 처리량과 연결해 동등 품질 성능 비율을 만들지 않는다. 가까운 SSIM도 지각적으로 같은 화질을 보증하지 않는다.

[Validation 확대 crop](analysis/validate/crops.png)에서 EXIF 6/8 방향·과일 질감·alpha 경계를, [quality 확대 crop](analysis/quality/crops.png)에서 두 engine의 대표 출력을 시각 확인했다. 이 예시는 전체 perceptual quality 평가를 대신하지 않는다.

이번 실행은 `effective-avif-q80-v1` 계약을 사용한다. 다섯 AWS Task의 별도 probe 80회에서 실제 Q80·speed 5·quality query 오류 0을 확인했고 성능 worker에는 probe를 넣지 않았다. 두 engine × 세 lossy 포맷 × 대표 8개, 총 48개 조합 모두 네 요청 Q의 출력 hash가 각각 달랐다. [검증 판정](assessment.json), [원본 trace를 재검사한 346 worker 기록](scope-call-audit.json).

AWS 이미지의 두 worker와 probe 바이너리는 수정본 로컬 전체 검증 및 [64회 실제 Q 조회](../quality-fix-20260910/README.md)에 사용한 바이너리와 byte 동일하다. [Binary provenance](binary-provenance.json). 이전 [b1의 AVIF quality 오류](../aws-20260910/README.md)는 별도 원자료로 보존하며 이번 비교에 합치지 않았다. Setter 수정·native 회귀 검증과 actual quality gate를 거친 새로운 source/image의 결과다.

측정 5,760개·calibration 1,152개·quality 중 Q80 48개의 hash/bytes가 validation과 일치했다. [측정 대조](measure-hash-check.json), [calibration 대조](calibration-hash-check.json), [quality 대조](quality-hash-check.json). 이 대조와 실제 quality 조회는 서로 다른 검증이다.

## 계측 범위·AWS 정리·비용

346개 worker 모두 self RSS와 개별 wait4 RSS가 같고 parent+worker scope가 대응했다. Warm-up을 포함한 전체 최대 native 호출은 **28.409초**였다. 본 측정의 최대 worker RSS는 libvips 934.516 / ImageMagick 941.898 MiB, cgroup sampled peak는 937.078 / 939.984 MiB였다. RSS에는 Go runtime·입력 preload·warm-up이 포함되며 baseline을 빼지 않는다. Cgroup은 parent·worker·file cache를 포함하는 50ms 표본으로 RSS와 측정 범위·시점이 다르다. [호출·scope 검사](scope-call-audit.json).

표준 Debian 패키지의 AVIF delegate 예외를 유지했다. AWS 실제 encoder threads는 libvips 1 / ImageMagick 2이며 로컬의 1 / 28과 구분한다. Fargate container-visible CPU quota는 `-1`이고 1 vCPU는 ECS Task 할당값이다. 상위 kernel quota를 추정하거나 동일 encoder threads의 비교로 표현하지 않는다.

ECR bootstrap 1개와 저장된 full plan 20개를 생성했다. 변경/삭제 없는 plan을 검토하고 apply 후 drift 없음, inbound 없음·TCP443 egress, S3 public blocks·암호화, 실제 image digest와 IAM 범위를 확인했다. **03:37 KST에 21개 자원을 제거**, Terraform·EC2/S3/ECR/IAM/Logs/ECS/ENI 독립 잔여 검사에서 모두 0개를 확인하고 watchdog도 종료했다. [Plan](raw/reviewed-plan.json), [실제 설정](raw/live-verification.json), [cleanup](raw/cleanup.json), [잔여 검사](raw/residual-check.json).

보수적 사전 견적은 US$1.50, 허용 조건은 US$3 이내였다. ECR image는 168,654,878 bytes, S3 결과는 2,166개·106,731,634 bytes다. Task pull 시작~정지 시각의 CPU·memory 추정 소계는 **US$0.205638**이며 IPv4·S3·ECR·로그·전송·세금 등을 제외한 값이다. 03:33 KST 조회한 UTC 09-10 계정 전체 Usage는 **US$0.0559455727, Estimated=true**였다. 집계 지연과 다른 실행이 섞인 잠정값으로 이번 c2 비용이나 확정 invoice가 아니다. [사용량·비용 관측](cost-observation.json), [단가와 전체 배포 모델](../../../deploy/e2-transformer-ab/COST.md).

2026-09-12에 운영자가 확인한 Cost Explorer 일별 CSV의 **2026-09-10 UTC 계정 사용료는 약 US$0.5304**였다(소수 넷째 자리까지 전달된 관측값). 이 날짜에는 a1 calibration, b1 전체 실행과 c2 재실행이 포함되므로 c2 Task 하나의 비용으로 해석하지 않는다. README의 E2 약 US$0.53은 이 실행일의 집계다. [후속 비용 관측](cost-observation.json)의 `billing_next_day`에 조회일·범위·정밀도를 기록했다. 월말 확정 invoice는 아니다.

## 결정·한계·재검토 조건

현재 workload에서 ImageMagick으로 전면 교체할 근거는 충분하지 않아 libvips를 유지한다. 처리량 이점은 뚜렷하지만 메모리 절감의 보편적 승자는 아니며, alpha/JPEG처럼 ImageMagick의 bytes·SSIM 이점이 있는 유형은 별도 선택의 근거가 될 수 있다. 실제 트래픽에서 이런 입력의 비중이나 특정 기능 요구가 커지면 해당 품질점에서 성능을 다시 측정해 재검토한다.

각 측정 조건은 한 Fargate Task 안의 다섯 반복이며 다양한 호스트의 반복 표본이 아니다. 24입력·8개 품질 대표의 작은 corpus, 특정 codec/preset, Q16 non-HDRI, 1 vCPU·2 GiB에 한정한다. CPU/memory 확대, 512MiB/1GiB 경계, 서비스 HTTP 부하·tail latency, encoder별 동등 지각 품질 성능은 이번 결과로 확정하지 않는다. E1 경로를 E2 buffer-thumbnail로 통합하는 서비스 변경도 별도 검증 대상이다.

## 원자료에서 재생성

회수한 2,166개 run 파일은 byte 그대로 보존했고 AWS metadata는 계정 식별자가 없는 허용 필드만 추출했다. [SHA-256 목록](raw/sha256.json)의 2,172개 export 파일을 검증했다. 별도 network-none 분석 실행에서 **29개 산출물**(분석 22개·차트/표 6개·집계 1개)이 동일 SHA-256으로 재생성됐다. [독립 대조](reanalysis-check.json). `.gitattributes`는 hash가 기록된 corpus·원자료의 개행을 보존한다. [시각 확인·개행 보존 검사](publication-verification.json)에 확인한 파일을 기록했다. 파생 hash 검증 JSON 세 개도 UTF-8/LF로 고정해 Windows·Linux 재생성 bytes가 일치함을 확인했다.

공개 저장소 root에서 Git LFS 파일을 확보하고 실행한다. 모든 출력 경로는 새 이름을 사용한다.

```powershell
docker build -f experiments/e2-transformer-ab/Dockerfile.tools -t e2-tools:local .
$evidence='reports/e2-transformer-ab/aws-20260910-c2'
foreach($mode in @('diagnose','validate','calibrate','measure','quality')) {
    docker run --rm --network none --cpus=2 --memory=2g --mount "type=bind,source=$((Get-Location).Path),target=/repo" e2-tools:local experiments/e2-transformer-ab/analyze.py "$evidence/raw/aws-$mode-20260910-c2" "dist/e2-reproduce-$mode"
    if($LASTEXITCODE -ne 0){throw 'Analysis failed.'}
}
python experiments/e2-transformer-ab/checkpoint.py "$evidence/raw" dist/e2-reproduce-summary.json
python experiments/e2-transformer-ab/check_outputs.py dist/e2-reproduce-validate dist/e2-reproduce-measure dist/e2-reproduce-hashes.json
docker run --rm --network none --cpus=2 --memory=2g --mount "type=bind,source=$((Get-Location).Path),target=/repo" e2-tools:local experiments/e2-transformer-ab/report.py dist/e2-reproduce-measure dist/e2-reproduce-quality dist/e2-reproduce-figures
```
