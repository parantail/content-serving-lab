# E2 — libvips와 ImageMagick 직접 호출 비교

설계 확정일: 2026-09-09

상태: **AWS 5회 측정·품질 실행·정리 완료, ImageMagick AVIF quality 전달 오류로 E2 최종 판정 보류**.

[로컬 AVIF 진단](diagnostics/README.md)에서 같은 1 vCPU·2 GiB 조건의 encoder threads 설정이 libvips 1, ImageMagick 28로 확인됐다. **2026-09-10 확정: 표준 Debian 패키지를 유지하고 동일 CPU·memory·요청 동시성 아래 배포 후보의 실제 동작을 비교한다.** AVIF delegate의 기본 thread 설정은 native thread 목표 1의 명시적 예외다. Fargate에서도 실제 값을 진단하고, 이 차이를 라이브러리 자체의 우열이나 동일 encoder thread 비교로 해석하지 않는다.

> 같은 이미지 묶음과 1 vCPU·2 GiB 제한에서 두 배포 후보의 처리량, 메모리, 출력 품질과 파일 크기는 어떻게 달라지는가?

이 문서는 실험의 기술 계약이다. [Corpus와 재현 명령](fixtures/README.md), [manifest](fixtures/generated/manifest.json), 두 native adapter·supervisor·독립 analyzer와 [runtime 이미지](../../Dockerfile.e2), [E2 Terraform](../../deploy/e2-transformer-ab/README.md)을 구현했다. [최신 AWS 기록](../../reports/e2-transformer-ab/aws-20260910/README.md)은 diagnose/validate/calibrate/measure/quality의 총 14,672회 실행, 159.711분의 5회 본 측정과 21개 자원 제거·잔여 0개를 보존한다. 그러나 ImageMagick AVIF의 Q50/65/80 출력이 같았고 같은 이미지의 로컬 진단에서 요청 Q80에 실제 Q50이 적용됨을 확인했다. **현재 adapter의 AVIF quality 전달은 미수정이며 추가 배포와 최종 선택을 보류한다.** 허용한 AVIF thread 차이와 별개의 계약 위반이다. 실행·decode·hash 검사 통과를 quality 적용 성공으로 해석하지 않는다.

[이전 AWS calibration](../../reports/e2-transformer-ab/aws-calibration-20260910/README.md)은 당시 60분 gate에서 중단한 별도 기록이다. 이후 210분·인프라 5시간으로 확대했으며 기존 calibration을 새 5회 본 측정에 합치지 않았다. [로컬 확인](results-local/preflight-20260910/README.md)을 포함해 같은 adapter의 AVIF Q 표기는 요청값이며, 실제 quality 검증 근거로 사용하지 않는다.

## 현재 기반과 구현 범위

현재 서비스의 [Transformer](../../internal/media/contracts.go)는 encoded bytes를 입력받고 결과 bytes를 반환한다. [govips 구현](../../internal/media/vips_transformer.go)과 [spec](../../internal/media/spec.go)은 cover/WebP를 지원하며, [기존 fixture](../e1-cache-stampede/fixtures/README.md)는 CC0 풍경 JPEG 한 장이다. [Dockerfile](../../Dockerfile)은 Go 1.26.7, govips v2.16.0, libvips 8.16.1을 사용하는 기반이다.

E2는 이 호출 구조를 바탕으로 두 adapter, corpus, batch runner, RSS 계측과 report pipeline을 구현한다. HTTP API 확장은 이번 완료 조건에 넣지 않는다. [E1 계측 계약](../e1-cache-stampede/AWS-S4-MEASUREMENT.md)의 cgroup reader와 [배포·정리 흐름](../../deploy/e1-aws-s4/README.md)을 참고하되 E2 프로세스·자원 구성으로 다시 검증한다.

초기 가설은 libvips의 thumbnail 경로가 큰 입력의 축소에서 자원 이점을 보일 수 있다는 것이다. 작은 입력, alpha 처리와 encoder 비용에 따라 이점이 달라질 수 있으며, 실제 선택은 아래 품질·성능·오류 결과에 따른다.

## 비교 방식

**Go → govips/libvips 대 Go → imagick v3/MagickWand/ImageMagick 7**로 비교한다. 현재 서비스의 in-process 호출 구조와 맞는다. MagickWand는 ImageMagick의 C API이고 imagick v3는 ImageMagick 7용 Go 바인딩이다. [MagickWand](https://imagemagick.org/magick-wand/), [imagick](https://github.com/gographics/imagick).

두 구현 모두 encoded bytes를 받아 decode → orientation/색 공간 정규화 → resize/crop → encode → output bytes 반환까지 측정한다. 원본 다운로드, HTTP, S3, derivative cache, 결과 업로드와 품질 분석은 측정 구간 밖에 둔다. 결과는 변환 경로의 성능이며 전체 HTTP 서비스 latency로 표현하지 않는다.

공통 container image 안에 engine별 worker binary를 두고 한 번에 하나만 실행한다. worker는 batch 동안 유지하고 이미지마다 native API를 직접 호출한다. batch 사이 프로세스 재시작은 RSS 초기화와 timeout 회수를 위한 실험 장치다. 이미지마다 CLI process를 생성하는 비교가 아니다.

### 라이브러리 호출 경로

E1은 `NewImageFromBuffer → ImageRef.Thumbnail`이고 govips v2.16.0 소스는 이를 `vips_thumbnail_image`에 연결한다. 같은 버전은 `vips_thumbnail_buffer` 경로도 제공한다. libvips의 thumbnail API는 loader의 shrink-on-load를 활용할 수 있다. 기존 호출이 동일 최적화를 수행한다고 가정해서는 안 된다. [thumbnail](https://www.libvips.org/API/8.17/ctor.Image.thumbnail.html), [thumbnail_buffer](https://www.libvips.org/API/8.17/ctor.Image.thumbnail_buffer.html).

E2의 주 비교는 배포 후보 구현으로 하고 libvips에는 버퍼 thumbnail API를 사용한다. ImageMagick도 공개 API의 thumbnail/resize 경로와 적용 가능한 loader hint를 검토한다. 같은 geometry/품질 계약을 맞추되 각 라이브러리의 정상적인 최적화는 허용하고 옵션을 기록한다. 비교의 의미는 특정 배포 후보 두 개의 비교다. E1 결과는 그대로 보존하고 기존 경로는 대표 JPEG→WebP 조건에서 연결 확인만 한다.

ImageMagick은 trixie의 Q16 non-HDRI 패키지를 사용한다. 현재 이미지의 core와 맞추며 Q8 전용 소스 빌드를 추가하지 않는다. Quantum depth/HDRI 여부는 메모리에 영향을 주므로 명시하고, 이 결과를 ImageMagick 모든 빌드의 결론으로 확대하지 않는다. [Debian Q16 개발 패키지](https://packages.debian.org/trixie/libmagickwand-7.q16-dev), [ImageMagick architecture](https://imagemagick.org/architecture/).

## Corpus — 정상 입력 24개

| 묶음 | 구성 | 수 |
| --- | --- | ---: |
| 사진 | 기존 풍경 + 질감이 다른 CC0 정물 사진 1종, 각 작은/큰 버전, JPEG/PNG/WebP/AVIF | 16 |
| alpha | 자체 생성한 도형·경계·반투명 gradient 1종, 작은/큰 버전, PNG/WebP/AVIF | 6 |
| orientation | 비대칭 식별 패턴을 가진 JPEG, EXIF 6/8 각 1개 | 2 |
| 합계 | 포맷 변형은 별도 콘텐츠로 세지 않음 | 24 |

- 작은 입력은 장변 1024px, 큰 입력은 장변 약 4096px로 하며 원본을 확대해 고해상도 사진인 것처럼 만들지 않는다. E1 원본은 별도의 연결 확인 fixture로 보존한다.
- 일반 입력은 8-bit sRGB 정지 이미지로 제한한다. ICC가 있는 입력은 sRGB 변환 후 metadata를 제거하고, orientation은 픽셀에 먼저 반영한다. HDR·CMYK·animation·다중 page는 이번 주 비교에서 제외한다.
- 두 번째 사진은 CC0 과일 정물 5040×3234이며 24개 입력·대표 품질 입력 8개·생성 옵션·hash는 [corpus](fixtures/README.md)에 고정했다.
- 생성된 WebP/AVIF 등을 실제 사용자 원본으로 표현하지 않는다. 이 작은 묶음의 균등 가중 결과와 입력 유형별 결과를 함께 보고한다. production 분포의 대표성을 주장하지 않는다.
- 정상 입력 밖에 잘린 파일, 지원하지 않는 형식, 공통 크기 제한 초과 입력의 소규모 검증 세트를 둔다. 악성 파일 전체 대응이나 fuzzing 실험으로 범위를 넓히지 않는다.

## 변환·측정 matrix

geometry는 (1) 장변 640으로 비율 유지 resize, (2) 640×480 안에 비율 유지 fit/contain, (3) 중앙 crop으로 640×640 cover 세 가지다. upscale은 하지 않는다. contain은 여백을 채우지 않는다. alpha는 PNG/WebP/AVIF에서 보존하고 JPEG에서는 흰색 배경에 합성한다. 크기 반올림과 crop 좌표는 공통 계약으로 고정한다.

| 구분 | 확정 조건 |
| --- | --- |
| 정상 처리량 | 24입력 × 3 geometry × 4 출력 포맷 × concurrency 1/4 × 5회 × 2 engine = 5,760 측정 변환 |
| 품질·bytes curve | 사전에 정한 대표 8입력 × cover 640 × JPEG/WebP/AVIF × Q50/65/80/90 × 2 engine = 192 출력 |
| PNG | lossless, compression 6, palette 축소 없음; lossy quality curve에 섞지 않음 |
| 반복 단위 | engine/geometry/output/concurrency 조합마다 새 worker; 24개 corpus 1회 warm-up 후 동일 24개 측정 |
| 실행 순서 | 두 engine을 같은 조건에서 짝지어 실행; 반복마다 A→B/B→A 교대, fixture 순서는 고정 seed |
| 자원 | Linux amd64, 1 vCPU·2 GiB, Go/native/codec 병렬 설정 기록; native thread 목표 1, AVIF delegate는 표준 패키지 기본값 예외 |
| 주 인코딩 조건 | JPEG/WebP/AVIF Q80. JPEG subsampling, WebP method, AVIF speed/encoder는 명시적으로 맞추고 사전 검증 |
| 시간 제한 | 변환 시작부터 30초, 본 측정 210분(다른 mode 60분), 인프라 최대 5시간 |

5,760회는 warm-up·품질 출력·검증을 제외한 값이다. Calibration은 동일 48조건을 한 반복씩 warm-up 포함 실행한다. **Calibration 전체 batch 실행 wall time × 5 × 1.25가 12,600초 이내**여야 본 측정을 시작한다. 이는 25% 시간 여유를 둔 실행 gate이며 완료 보장은 아니다. Parent의 최초 corpus 검증·manifest 준비는 batch 실행 구간 앞에 있으며 Task 총시간과 인프라 deadline에는 포함한다. 맞지 않으면 실행을 중단하고 측정 전에 설계를 재검토한다.

호출 수를 모드별로 고정한다: diagnose 16, validate 576, calibrate 2,304(측정 1,152 + warm-up 1,152), measure 11,520(측정 5,760 + warm-up 5,760), quality 192. diagnose 외 네 mode는 같은 Task에서 추가로 16회씩 AVIF 설정을 사전 진단한다. AWS 한 사이클의 최대 예정 호출은 **14,672**이다. 개발 테스트와 E1 연결 확인 2회는 이 수에 섞지 않는다. 각 mode 시작 전에 전체 job/seed/호출 수를 manifest로 저장한다. 로컬 확인·calibration과 AWS 측정은 별도 cohort다.

libvips operation cache는 E1처럼 끈다. ImageMagick의 pixel cache는 이미지 연산용 저장 공간이므로 동일한 이름의 캐시라고 끌 수 없다. 주 비교는 pixel cache의 디스크 spill을 금지하고 cgroup으로 전체 메모리를 제한한다. native thread 제한만으로 delegate thread까지 제한되었다고 단정하지 않고 codec 설정·관측 thread도 점검한다. ImageMagick 자체의 memory limit은 모든 native 메모리를 제한하는 값이 아니다. [자원·pixel cache 설명](https://imagemagick.org/architecture/).

2 GiB 외에 512 MiB/1 GiB sweep이나 별도 OOM 유발 실험은 기본안에 넣지 않는다. 동일 제한에서 발생한 OOM·timeout·resource-limit 오류는 결과에 남긴다. 추가 메모리 경계 실험은 현재 범위에 포함하지 않는다.

## 계측·품질·판정

- 처리량: batch의 성공 변환 수 / 측정 wall time. 실패 개수와 함께 보고한다.
- latency: slot 진입 후 encoded bytes를 받고 native 객체를 해제할 때까지; queue wait는 별도다. batch별 p50/p95와 반복 범위를 보고한다. 입력별 표본은 5개이므로 입력별 p95를 강한 근거로 쓰지 않고 중앙값·전체 관측값을 보존한다.
- RSS: worker별 Linux `getrusage(RUSAGE_SELF).ru_maxrss`, 종료 시 개별 child의 wait4 rusage로 교차 확인한다. Linux 단위 KiB를 bytes로 변환한다. worker 수명 전체의 high-water mark이며 runtime·입력 preload·warm-up도 포함한다고 명시한다. parent의 누적 RUSAGE_CHILDREN 값을 해당 batch 값으로 사용하지 않는다. [getrusage](https://man7.org/linux/man-pages/man2/getrusage.2.html).
- cgroup CPU/memory는 E1 reader를 활용하되 parent와 worker를 포함하는 새 scope를 다시 진단한다. E1의 self 단독 프로세스 검증을 그대로 통과했다고 표현하지 않는다. cgroup sampled max와 RSS는 별도 열이다.
- `context` timeout만으로 C 호출이 종료되었다고 처리하지 않는다. supervisor가 시작/완료 이벤트를 추적하고 30초 초과 시 worker를 종료한다. 다른 in-flight 작업은 timeout과 구분해 interrupted로 남긴다. OOM 판정은 exit 137만으로 하지 않고 cgroup/Task 종료 근거를 확인한다. 실행 오류 뒤 후속 run은 중단하고 원자료를 회수한다.
- 품질은 양쪽 출력에 공통인 독립 reference와 SSIM/PSNR을 사용하고 대표 확대 crop도 확인한다. [analyze.py](analyze.py)는 encoded-sRGB RGB, SSIM window 7·uniform weight·sample covariance·channel 평균·data_range 255를 사용한다. 검정/흰 배경 합성은 float RGB에서 계산하며 PSNR은 같은 RGB의 MSE로 계산한다(완전 일치는 JSON null/+∞). 같은 Q 숫자를 같은 화질로 취급하지 않는다. SSIM은 사람의 평가를 완전히 대체하지 않는다. [SSIM 연구](https://ece.uwaterloo.ca/~z70wang/research/ssim/), [metric 구현 참고](https://scikit-image.org/docs/stable/api/skimage.metrics.html#skimage.metrics.structural_similarity).
- reference는 A나 B의 압축 출력이 아니라 원본의 정규화·geometry를 독립 구현으로 계산한 lossless raster를 사전 생성한다. resize 구현 차이도 포함한 최종 이미지 품질임을 명시한다. alpha는 검정/흰색 배경 합성 후 두 점수를 모두 보고하고 alpha plane 오차도 별도 확인한다. reference 생성기·필터·metric window/색 공간을 본 측정 전에 고정한다.
- 품질 분석은 성능 worker와 분리해 종료 후 실행한다. 처리량 측정의 output bytes/hash와 curve용 실제 출력을 보존하고 한 engine의 출력만 reference로 삼지 않는다.

선택 규칙: 먼저 geometry/orientation/alpha와 제한 정책을 만족해야 한다. 그다음 같은 출력 포맷 안에서 비슷한 품질의 관측점에 대한 bytes·처리량·RSS를 비교한다. Q sweep에 비교할 만한 품질 구간이 없으면 동등 품질의 승자를 선언하지 않는다. Q80 처리량은 설정 고정 결과이며 곧바로 동등 품질 성능이라고 쓰지 않는다.

초기 유지 기준은 현재 libvips다. ImageMagick으로 교체하려면 명확한 품질/지원 기능 이득 또는 반복 편차를 넘는 자원·처리량 개선이 있고, 다른 주요 지표의 손해를 설명할 수 있어야 한다. 단일 가중 점수로 보편적 승자를 만들지 않는다. 유형별 승자·상충·판단 유보를 허용한다. 현재는 단일 수치 문턱을 두지 않는다. 이후 판정 기준을 바꿀 때는 적용 전 변경 내용과 이유를 문서화하고 이전 결과와 구분한다.

예정 시각 결과는 처리량–RSS scatter(단위와 반복 범위), 포맷별 SSIM–bytes curve, 입력 유형별 속도/bytes/오류 matrix다. RSS는 batch 값이므로 입력별 RSS 승자를 도출하지 않는다.

## AWS 구성과 실행 조건

서울 `ap-northeast-2`의 Linux amd64 Fargate 일회성 Task 1개, 1 vCPU·2 GiB를 사용한다. 같은 Task 안에서 두 engine worker를 교대로 실행한다. 별도 ECS Service와 ALB는 두지 않는다. 변환 함수만의 비교에 HTTP와 저장소 latency를 섞지 않기 위한 구성이다.

새 E2 Terraform에 VPC/public subnet/IGW/route/security group, ECS cluster/task definition, immutable ECR, 결과 S3, IAM, CloudWatch Logs, S3 gateway endpoint를 만든다. corpus는 이미지에 고정하고 결과 S3에는 batch 종료 후 업로드한다. public IPv4로 image pull/log 전송 경로를 확보하며 inbound는 열지 않는다. NAT와 유료 interface endpoint는 사용하지 않는다. 기존 E1 state/resource와 섞지 않는다.

월간 US$10 Budget·알림을 재확인하고, 이번 배포 예상 비용 US$3 이내·인프라 최대 5시간을 실행 조건으로 사용한다. US$3은 실행 허용 기준이며 산출된 견적이나 실제 과금의 자동 차단 한도가 아니다. 실제 단가는 구현할 resource 수·보존량·소요 시간과 함께 apply 전에 계산한다. Fargate는 image 다운로드부터 종료까지의 자원 사용시간에 요금이 적용되며 S3/ECR/로그/IPv4 비용도 따로 포함해야 한다. [Fargate 요금](https://aws.amazon.com/fargate/pricing/).

E1에서 검증한 preflight → clean checkpoint → ECR bootstrap/push → 저장된 plan 검토/apply → 동일 이미지 진단 → calibration/본 측정 → 회수/독립 재분석 → destroy/잔여 검사 흐름을 E2에 맞춘다. watchdog는 bootstrap 전에 시작하고 deadline에 실행 중 Task를 중단·회수·제거한다. 기존 로컬 watchdog는 운영 PC가 계속 켜져 있고 AWS 인증이 유효해야 동작한다. AWS 자체 예약 정리는 새 범위이므로 포함한 것으로 간주하지 않는다.

Local/AWS raw는 분리한다. 동일 image와 설정이어도 Docker 환경과 Fargate의 성능 수치를 하나의 모집단으로 합치지 않는다. 단일 Fargate 배치 결과를 다양한 호스트에서의 일반적 성능으로 확대하지 않는다.

## 실행 전에 고정할 산출물

설계 확정은 파일 확보·동작 검증 완료를 의미하지 않는다. 아래 항목이 갖춰진 clean checkpoint에서만 본 측정을 실행한다.

| 단계 | 고정하거나 검증할 내용 |
| --- | --- |
| Corpus 확보 | 추가 CC0 사진의 정확한 출처·재배포 근거, 24개 fixture ID·해상도·포맷·bytes·SHA-256, 생성기와 생성 옵션, 대표 품질 입력 8개 ID |
| Build/adapter | imagick/native/codec의 정확한 버전, ImageMagick Q16 non-HDRI 확인, AVIF encoder와 실제 네 포맷 round-trip, 동일 codec 경로·thread 설정 |
| 변환·제한 정책 | 크기 반올림·crop 좌표, 최대 입력 bytes/pixels, JPEG subsampling·WebP method·AVIF speed, native/pixel-cache 자원 제한 |
| 품질 기준 | 독립 reference 생성기·버전·필터, metric window/색 공간·PSNR 정의, alpha 검사와 시각 확인 입력 |
| Harness/calibration | seed·raw schema·warm-up 포함 최대 호출 수, 오류/timeout 회수, RSS와 cgroup scope, 210분 이내 본 측정 실행 가능 여부 |
| AWS 사전점검 | 현재 인증·Budget·quota·잔여 자원, 자원별 비용 계산, 생성 자원 plan, image digest·watchdog·회수/제거 경로 |

각 항목은 공개 파일과 명령으로 확인 가능해야 한다. 예정된 파일의 hash나 버전을 추정해서 채우지 않는다. 합의된 범위·제한의 변경이 필요하거나 실행이 막히면 후속 측정을 중단하고 사유를 기록한다. AWS 자원이 이미 존재하면 가능한 원자료 회수와 정리를 우선한다.

## 재현 경로와 완료 조건

현재 구현의 실제 명령·원자료 형식은 [RUN.md](RUN.md)에 있다.

다음 순서로 구현하며, 해당 기능을 검증할 때 실제 명령과 산출물 경로를 추가한다.

1. Corpus를 확보하고 license/hash·생성 재현성을 확인한다.
2. 동일 입력 계약의 adapter와 runner/analyzer를 구현하고 container에서 로컬 검증한다.
3. Calibration 결과와 실제 실행 조건을 고정하고 clean checkpoint를 만든다.
4. E2 인프라를 셋업하고 plan/apply 후 동일 이미지 진단과 본 측정을 실행한다.
5. 원자료를 회수하고 별도 분석 실행으로 표·차트가 재생성되는지 대조한다.
6. 즉시 destroy하고 Terraform state와 서비스 API로 잔여 자원을 확인한다.
7. 결과·한계·선택·재검토 조건과 비용 근거를 보고서에 남긴다.

완료에는 전체 예정 호출의 성공/오류/중단 분류, 두 engine의 결과 정확성 검증, 원자료에서 재생성한 세 시각 결과, 독립 재분석, AWS 제거 근거가 필요하다. 실패·무효 run을 숨기거나 좋은 결과만 골라 반복을 보충하지 않는다. 측정 결과와 실제 비용 기록에는 조회 시점과 범위를 표시한다.
