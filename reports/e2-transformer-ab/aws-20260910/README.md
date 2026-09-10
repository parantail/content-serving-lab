# AWS E2 — 실행은 완료했지만 AVIF 품질 설정 오류로 비교 판정 보류

**E2는 미완료다.** 5회 본 측정과 품질 sweep까지 실행·회수·AWS 제거를 마쳤지만, ImageMagick AVIF의 요청 Q가 encoder에 전달되지 않았다. 같은 실행 이미지의 로컬 진단에서 **요청 Q80 → 실제 libvips Q80 / ImageMagick Q50**을 확인했다. AVIF를 포함한 최종 변환기 선택과 동등 조건 성능 주장을 보류한다. 오류를 수정한 실행으로 이번 측정을 대체하지 않았으며 원자료를 그대로 보존했다.

![품질 조건 오류를 포함한 진단용 SSIM–bytes 관측](figures/quality-bytes.png)

## 확인한 문제와 영향

AWS quality sweep의 대표 입력 8개 모두에서 ImageMagick AVIF의 Q50·65·80 출력이 **동일 SHA-256·bytes**였다. Q90에서 달라진 출력만으로 quality 전달 성공을 판단할 수 없다. Adapter는 Q90 이상에서 chroma를 4:4:4로 바꾸므로 별도의 변수가 함께 바뀐다. [품질 조건 판정](assessment.json)에 입력별 동일 hash와 로컬 진단을 기록했다.

| 확인 | libvips | ImageMagick |
| --- | ---: | ---: |
| 로컬 동일 이미지에서 요청한 AVIF Q | 80 | 80 |
| encode 직전 조회한 실제 quality | 80 | **50** |
| 성공한 quality 조회 | 8/8 | 8/8 |
| AWS Q50·65·80 출력이 동일한 품질 대표 입력 | 0/8 | **8/8** |

Adapter는 `SetImageCompressionQuality`와 문자열 `SetOption("quality", ...)`를 호출한다. 하지만 이 버전의 AVIF writer는 `image_info->quality`를 읽으며, image의 quality 필드를 설정하는 것과 다르다. `MagickSetCompressionQuality`는 writer가 읽는 필드를 설정한다. 이는 adapter의 API 사용 오류이며, 앞서 허용한 **표준 패키지의 AVIF threads 차이**와 별개의 문제다. [Image quality setter](https://github.com/ImageMagick/ImageMagick/blob/7.1.1-43/MagickWand/magick-image.c#L10279), [AVIF writer](https://github.com/ImageMagick/ImageMagick/blob/7.1.1-43/coders/heic.c#L1385), [Wand quality setter](https://github.com/ImageMagick/ImageMagick/blob/7.1.1-43/MagickWand/magick-property.c#L2001).

실제 quality 조회는 **AWS 실행과 같은 digest를 로컬에서 실행한 별도 진단 16회**다. AWS Task에서는 quality parameter를 직접 조회하지 않았으므로 로컬 관측을 AWS 직접 계측으로 표현하지 않는다. AWS의 품질별 출력 동일성과 고정된 writer 코드가 함께 문제를 뒷받침한다. 진단은 설정을 변경하지 않고 encode 직전에 값을 읽는다. [진단 C 코드](quality-parameter/codec_trace.c), [ImageMagick 조회](quality-parameter/magick.log), [libvips 조회](quality-parameter/vips.log), [진단 원자료 hash](quality-parameter/sha256.json).

공개 진단 명령도 별도로 16회 재실행해 같은 quality 조회와 출력 hash를 확인했다. 최초/재현 진단 출력 16개는 AWS quality의 요청 Q80 출력과도 일치했다. 두 로컬 실행 총 32회는 AWS 호출 수에서 제외한다. [진단 재현 대조](quality-parameter/reproduction-check.json).

Native exit 0, decode 성공, geometry/alpha 검사와 출력 hash 일치는 **요청 quality 적용**까지 증명하지 않았다. 같은 잘못된 adapter로 만든 validation과 measurement의 hash가 일치할 수 있다. 원자료와 기존 analyzer의 `valid=true`는 실행·coverage·decode 검사 범위이며, 최종 실험 유효성은 **`assessment.json`의 `quality_contract_valid=false`**로 구분한다.

## 실행 환경과 완료 범위

질문은 같은 입력과 1 vCPU·2 GiB에서 Go native binding으로 호출한 두 배포 후보의 처리량·RSS·품질·파일 크기가 어떻게 달라지는가다. 초기 가설은 libvips thumbnail의 축소 최적화 이점이었다. Geometry/orientation/alpha 및 설정 계약을 먼저 통과한 뒤 유사 품질의 관측점을 비교한다는 [사전 선택 기준](../../../experiments/e2-transformer-ab/README.md)을 유지한다. 이번에는 quality 전달 오류 때문에 선택 단계로 넘어가지 않는다.

| 항목 | 실제 실행 |
| --- | --- |
| 날짜·리전 | 2026-09-10, 서울 `ap-northeast-2` |
| 환경 | Linux amd64, Fargate 1.4.0, Task 할당 1 vCPU·2 GiB, 한 번에 Task 1개 |
| Source commit | `e7bb303b5a5f94dc2395aec77a09eb49173aa2cc` |
| Image digest | `sha256:cb2c47d022febbd800f49d9c02d48afcfc81e42f416276e163184d57d21a3c98` |
| 호출 | Go 1.26.7 → govips 2.16.0 / libvips 8.16.1, imagick 3.7.3 / ImageMagick 7.1.1.43 Q16 non-HDRI |
| AVIF | libheif 1.19.8 / AOM 3.12.1, speed 5; 실제 quality 불일치 |
| Corpus | 24입력, 품질 대표 8개; SHA-256 `127390f507c0d3918b2097eb081511b2952715cc5ac1fbe17683c7745cf5a458` |
| Geometry | 장변 640 resize, 640×480 contain, 중앙 640×640 cover; upscale 없음 |
| 본 측정 | 세 geometry × 네 출력 × 요청 동시성 1/4 × 두 engine × 5회 = 240 batch |
| Warm-up | 각 성능 batch의 24입력 1회 후 동일 24입력 측정; 반복별 engine 순서 교대, seed 209~213 |
| 제한 | Native 30초, 본 측정 loop 210분, 인프라 최대 5시간 |

사진·합성 alpha·EXIF 패턴과 사용 조건은 [corpus](../../../experiments/e2-transformer-ab/fixtures/README.md)에 있다. 포맷 변형 24개를 서로 다른 실제 콘텐츠 24종으로 표현하지 않는다. Batch마다 worker를 재시작하지만 각 이미지 변환은 라이브러리 직접 호출이며, 이미지마다 CLI를 실행하는 비교가 아니다. HTTP·S3·원본 다운로드는 native timing 밖이다. 현재 서비스의 E1 transformer 경로는 변경하지 않았다.

| AWS mode | Batch | 측정/출력 성공 | Warm-up 성공 | 별도 같은 Task 진단 | Loop 초 |
| --- | ---: | ---: | ---: | ---: | ---: |
| diagnose | 2 | 16 | 0 | 0 | 24.499 |
| validate | 24 | 576 | 0 | 16 | 447.557 |
| calibrate | 48 | 1,152 | 1,152 | 16 | 1,875.453 |
| measure | 240 | 5,760 | 5,760 | 16 | 9,582.665 |
| quality | 24 | 192 | 0 | 16 | 158.158 |

**총 14,672회가 실행상 성공했고, native 오류·timeout·interrupted는 0회다.** 별도 로컬 quality 진단 16회는 이 합계에 넣지 않는다. Calibration 31.258분 × 5 × 1.25 = 195.360분으로 당시 210분 gate를 통과했고 본 측정 loop는 159.711분에 끝났다. 이 시간도 잘못된 ImageMagick AVIF quality 조건의 관측이므로 수정본의 예상 시간으로 확정할 수 없다. [집계](summary.json), [실행 metadata](raw/execution.json), [진입 gate](raw/calibration-gate.json).

## 보존한 성능 관측 — 최종 선택 근거로 사용하지 않음

![요청 Q80 조건의 진단용 처리량–RSS](figures/throughput-rss.png)

아래는 cover640, 24입력 batch의 5회 중앙값이다. 처리량은 성공 변환/초, RSS는 worker 자체 lifetime high-water mark의 MiB다. **AVIF는 요청 Q80이 일치해도 실제 encoder quality가 일치하지 않은 조건**이다. 전체 48개 engine별 집계와 min/max 범위는 [summary](figures/summary.json), 24개 조건 쌍은 [conditions](figures/conditions.json)에 있다.

| 출력 | 동시성 | libvips 처리량 | ImageMagick 처리량 | libvips RSS | ImageMagick RSS |
| --- | ---: | ---: | ---: | ---: | ---: |
| JPEG | 1 | 4.423 | 1.832 | 536.4 | 407.4 |
| JPEG | 4 | 3.519 | 1.283 | 771.9 | 808.5 |
| PNG | 1 | 3.624 | 1.454 | 539.1 | 415.6 |
| PNG | 4 | 2.995 | 1.022 | 812.2 | 735.7 |
| WebP | 1 | 3.630 | 1.655 | 513.5 | 400.2 |
| WebP | 4 | 2.751 | 1.141 | 835.8 | 731.9 |
| AVIF — quality 불일치 | 1 | 0.683 | 0.524 | 486.4 | 494.8 |
| AVIF — quality 불일치 | 4 | 0.443 | 0.416 | 759.3 | 885.8 |

JPEG/PNG/WebP의 요청 Q80 고정 처리량 중앙값 비는 조건별 libvips/ImageMagick 2.19~3.19였다. RSS는 libvips가 항상 낮지 않았다. 동시성 4의 처리량 중앙값은 같은 engine/geometry/format의 동시성 1 대비 0.649~0.827배였다. 이는 이번 1 vCPU 배치의 관측이며, 서비스 전체 latency·최적 동시성이나 동등 화질 처리량을 증명하지 않는다. 각 batch의 p50/p95와 개별 관측은 [분석](analysis/measure/batches.json)에 있다.

![입력별 요청 Q80 진단용 duration·bytes 비교](figures/input-matrix.png)

입력별 duration/bytes는 반복 중앙값의 ImageMagick/libvips 비를 log2로 표시했다. 양수 duration은 libvips가 빠름, 양수 bytes는 libvips가 작음을 뜻한다. 숫자는 실제 log2 값이고 색은 ±2에서 포화된다. 예를 들어 일부 작은 입력의 AVIF는 ImageMagick이 빠르지만 quality 불일치 때문에 우위 판정에 사용할 수 없다. RSS는 batch 단위여서 입력별 RSS로 나누지 않는다.

## 품질·정확성 관측

576개 validation 출력과 192개 quality 출력을 독립 디코딩해 geometry·alpha·JPEG 4:4:4·PNG truecolor를 확인했다. [품질 값](analysis/quality/quality.json)은 독립 lossless reference와 encoded-sRGB RGB SSIM(window 7, uniform, sample covariance, 채널 평균)·PSNR을 사용한다. Alpha는 검정/흰 배경 합성 점수와 alpha plane 오차를 별도로 기록한다. 그래프는 두 배경 SSIM의 최솟값을 보여준다. PSNR의 완전 일치는 JSON null로 표현한다.

품질 curve 자체에도 상충이 있다. `alpha-large-webp` → JPEG에서 요청 Q80의 ImageMagick은 22,009 bytes / SSIM 0.984632, libvips는 26,280 bytes / 0.982032였다. 같은 입력의 WebP는 libvips Q90 14,090 bytes / 0.982349와 ImageMagick Q80 17,046 bytes / 0.982755가 가까운 점수의 관측점이다. 이를 전체 입력·포맷의 동등 품질 승자로 확대하지 않는다. AVIF `fruit-small-png` 요청 Q80의 27,214 대 9,134 bytes는 각각 SSIM 0.969574 대 0.943554이며, 확인된 quality 전달 오류를 포함한다.

[Validation 확대 crop](analysis/validate/crops.png)에서 EXIF 6/8 방향·과일 질감·alpha 경계를, [quality 확대 crop](analysis/quality/crops.png)에서 EXIF 6·과일·alpha를 시각 확인했다. 확대 예시는 전체 perceptual quality 평가를 대신하지 않는다.

측정 5,760개·calibration 1,152개와 quality 중 Q80 48개의 hash/bytes는 해당 validation 출력과 일치했다. [측정 대조](measure-hash-check.json), [calibration 대조](calibration-hash-check.json), [quality 대조](quality-hash-check.json). 이 일치는 잘못된 quality 설정을 바로잡는 근거가 아니다.

## 계측 범위·정리·비용

346개 worker에서 self RSS와 개별 wait4 RSS가 같고 parent+worker scope 대응을 확인했다. 최대 native 호출은 26.556초였다. 본 측정의 최대 worker RSS는 libvips 878.496 / ImageMagick 978.648 MiB, cgroup sampled peak는 883.211 / 990.207 MiB였다. RSS에는 Go runtime·입력 hash 검증·preload·warm-up을 포함하며 baseline을 빼지 않는다. Cgroup은 parent·worker·file cache를 포함하는 50ms 표본이다. 서로 다른 메모리 지표를 같은 값으로 해석하지 않는다. [Scope·호출 수 검사](scope-call-audit.json).

모든 AWS Task에서 AVIF speed 5와 encoder threads libvips 1 / ImageMagick 2를 조회했다. 성능 worker에는 probe를 넣지 않았다. 같은 digest의 로컬 quality 진단은 threads 1 / 28이었으며 local/AWS timing을 섞지 않는다. Fargate의 container-visible CPU quota는 `-1`이고 1 vCPU는 ECS Task 할당값이다. 상위 kernel quota를 이 값에서 재구성하지 않는다.

ECR bootstrap 1개와 저장된 full plan 20개를 생성했다. Plan은 변경/삭제 0개, apply 후 drift 없음·inbound 없음·egress TCP443·실제 digest를 확인했다. **2026-09-10 19:46 KST에 21개 자원을 제거했고**, Terraform 및 EC2/S3/ECR/IAM/Logs/ECS/ENI 독립 검사에서 잔여 0개였다. 로컬 watchdog도 종료했다. [Plan](raw/reviewed-plan.json), [실제 배포](raw/live-verification.json), [cleanup](raw/cleanup.json), [잔여 검사](raw/residual-check.json).

배포 전 보수적 견적은 US$1.50, 허용 조건은 US$3 이내였다. ECR image는 168,653,619 bytes, S3 결과는 2,166개·103,515,646 bytes였다. Task pull 시작~정지 시각과 공개 단가로 계산한 CPU·memory 소계는 **약 US$0.195**이며 전체 비용이나 invoice가 아니다. 19:47 KST Cost Explorer의 같은 UTC 날짜 account-wide Usage 집계는 비어 있고 `Estimated=true`였다. 비용 0달러나 E2 귀속 확정값으로 해석하지 않는다. [사용량·비용 관측](cost-observation.json), [단가·배포 모델](../../../deploy/e2-transformer-ab/COST.md).

## 재생성과 다음 조건

Run 원자료의 bytes는 회수본 그대로 보존했고, AWS 실행 metadata는 계정 식별자를 제외한 허용 필드만 별도 추출했다. [SHA-256 목록](raw/sha256.json)은 이 파일 2,172개를 포함한다. [독립 재분석 대조](reanalysis-check.json)의 29개 산출물(분석 22개·figure/표 6개·집계 1개)이 일치했다. 현재 `report.py`는 모든 품질 입력의 Q50/65/80 출력이 동일하면 기본 비교 보고서 생성을 거부한다. 이는 이번 결함을 잡는 추가 검사이며 모든 품질 전달 오류를 검출한다는 보장은 아니다.

공개 저장소 root에서 Git LFS 파일을 확보하고 다음을 실행한다. 모든 출력 경로는 새 이름을 쓴다.

```powershell
docker build -f experiments/e2-transformer-ab/Dockerfile.tools -t e2-tools:local .
$evidence='reports/e2-transformer-ab/aws-20260910'
foreach($mode in @('diagnose','validate','calibrate','measure','quality')) {
    docker run --rm --network none --cpus=2 --memory=2g --mount "type=bind,source=$((Get-Location).Path),target=/repo" e2-tools:local experiments/e2-transformer-ab/analyze.py "$evidence/raw/aws-$mode-20260910-b1" "dist/e2-reproduce-$mode"
    if($LASTEXITCODE -ne 0){throw 'Analysis failed.'}
}
python experiments/e2-transformer-ab/checkpoint.py "$evidence/raw" dist/e2-reproduce-summary.json
python experiments/e2-transformer-ab/check_outputs.py dist/e2-reproduce-validate dist/e2-reproduce-measure dist/e2-reproduce-hashes.json
docker run --rm --network none --cpus=2 --memory=2g --mount "type=bind,source=$((Get-Location).Path),target=/repo" e2-tools:local experiments/e2-transformer-ab/report.py dist/e2-reproduce-measure dist/e2-reproduce-quality dist/e2-reproduce-figures --diagnostic-note 'INVALID AVIF QUALITY: diagnostic evidence only; no transformer selection'
```

Quality parameter 진단은 source `e7bb303`의 별도 checkout에서 [Dockerfile.e2](../../../Dockerfile.e2)의 runtime 이미지를 build해 준비한다. 현재 checkout에서는 `dev` target을 `e2-dev:local`로 build한 뒤 아래 명령으로 trace를 컴파일하고 같은 runtime 이미지의 worker를 직접 호출한다. Probe는 별도 local cohort이며 timing은 성능 결과로 사용하지 않는다.

```powershell
./reports/e2-transformer-ab/aws-20260910/quality-parameter/run.ps1 -Image <e7bb303-runtime-image> -RunId quality-parameter-reproduce
```

다음 실행에는 Wand quality 설정 수정, 실제 encoder quality 조회와 여러 Q의 출력 변화 검증이 먼저 필요하다. 수정 후 validation/calibration부터 새 source/image로 검증해야 한다. 처리 시간과 native 30초·본 측정 210분·인프라 5시간 gate를 다시 통과하는지 확인하기 전에는 재실행 가능하다고 단정하지 않는다. 이번 자료의 AVIF 제외, 실패 무시 또는 반복 자동 보충으로 E2를 완료 처리하지 않는다. [이전 60분 gate 중단 기록](../aws-calibration-20260910/README.md)은 별도 시도로 보존한다.
