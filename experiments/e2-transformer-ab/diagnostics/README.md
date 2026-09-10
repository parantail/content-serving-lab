# AVIF encoder configuration

상태: **로컬 native 설정 진단 완료 · 표준 패키지의 AVIF thread 차이를 포함한 비교로 확정**.

1 vCPU·2 GiB의 같은 Docker Linux 환경에서 Go adapter를 직접 호출했다. ImageMagick resource thread 1과 `OMP_NUM_THREADS=1`, `MAGICK_THREAD_LIMIT=1`을 설정해도 AV1 encoder의 설정까지 같아지지는 않았다.

| 구현 | 실제 AV1 encoder | threads 설정 | speed | 진단 입력 |
| --- | --- | ---: | ---: | ---: |
| govips v2.16.0 / libvips 8.16.1 | libheif 1.19.8 / AOM 3.12.1 | 1 | 5 | 품질 대표 8개 |
| imagick v3.7.3 / ImageMagick 7.1.1.43 Q16 non-HDRI | libheif 1.19.8 / AOM 3.12.1 | 28 | 5 | 동일 8개 |

threads는 encoder가 보고한 설정값이다. 동시에 28 CPU를 썼다는 뜻이나 실제 실행 thread 수의 시계열은 아니다. 두 컨테이너의 CPU quota는 1 vCPU다. 후속 [AWS 다섯 Task](../../../reports/e2-transformer-ab/aws-20260910/README.md)에서는 libvips 1/ImageMagick 2를 확인했다. 이 진단의 latency/RSS는 계측 라이브러리가 추가된 개발 build의 값이므로 A/B 성능 결과로 사용하지 않는다.

## 원인과 계측 방법

`heif_context_encode_image` 직전에 `heif_encoder_get_parameter_integer`로 threads와 speed를 읽었다. `codec_trace.c`는 원본 encode 함수를 그대로 호출하며 설정이나 픽셀은 변경하지 않는다. 두 query의 오류 코드는 모두 0이었다. 원자료는 [vips 설정](vips-codec.log), [ImageMagick 설정](magick-codec.log), [환경](environment.json)에 있다.

libvips는 libheif encoder의 threads를 `vips_concurrency_get()`으로 지정한다. [libvips 8.16.1 source](https://github.com/libvips/libvips/blob/v8.16.1/libvips/foreign/heifsave.c).

ImageMagick 7.1.1.43의 HEIC/AVIF writer는 speed/chroma 옵션을 전달하지만 같은 경로에 encoder threads 전달이 없다. libheif 1.19.8 AOM plugin의 기본값은 `std::thread::hardware_concurrency()`를 사용한다. [ImageMagick source](https://github.com/ImageMagick/ImageMagick/blob/7.1.1-43/coders/heic.c), [libheif AOM source](https://github.com/strukturag/libheif/blob/v1.19.8/libheif/plugins/encoder_aom.cc).

## 재현

공개 저장소 root의 PowerShell 7에서 실행한다. 생성된 binary·JSONL·임시 shared library는 Git 제외 `dist/`에 보존된다.

```powershell
docker build -f Dockerfile.e2 --target dev -t e2-dev:local .
docker run --rm --network none --mount "type=bind,source=$((Get-Location).Path),target=/src" -w /src e2-dev:local sh -c 'go test ./... && go vet ./... && go test -tags=e2integration ./internal/e2'
docker run --rm --network none --mount "type=bind,source=$((Get-Location).Path),target=/src" -w /src e2-dev:local sh -c 'go build -buildvcs=false -o dist/e2-vips ./cmd/e2-vips && go build -buildvcs=false -o dist/e2-magick ./cmd/e2-magick && gcc -shared -fPIC -o dist/e2-codec-trace.so experiments/e2-transformer-ab/diagnostics/codec_trace.c -lheif -ldl'
./experiments/e2-transformer-ab/diagnostics/run.ps1
```

2026-09-10 확정한 비교는 동일 CPU/memory·요청 동시성 제한 아래 표준 패키지의 실제 동작이다. ImageMagick native 빌드/패치를 추가하지 않으며 AVIF delegate 기본 thread 수는 명시적 예외다. 후속 본 측정·AWS 배포·제거는 완료했지만 **별개의 AVIF quality 전달 오류로 최종 판정을 보류**했다. 기존 probe는 threads/speed만 읽어 이 결함을 잡지 못했다. [실제 quality 조회 코드와 관측](../../../reports/e2-transformer-ab/aws-20260910/quality-parameter/codec_trace.c)을 참고한다.

Native integration test는 E2 이미지의 AVIF encoder를 사용하므로 `e2integration` tag로 실행한다. 기존 E1 이미지의 기본 테스트에 AVIF encoder 설치를 요구하지 않는다. 테스트는 두 engine의 작은 입력·세 geometry·네 encode 경로, JPEG 흰 배경 합성과 PNG/WebP alpha, invalid 입력 거부를 확인한다. 수정본은 ImageInfo quality setter를 함께 사용하고 동일 4:2:0의 Q50/Q80 출력 차이를 회귀 검사한다. 현재 probe는 threads/speed에 실제 quality query를 추가했다. `quality-sweep.ps1 -Image <runtime-image> -RunId <fresh-id>`는 대표 8개에 네 Q를 실제 조회하며, 새 diagnose/preflight는 8개 모두 실제 Q80이어야 통과한다. 과거 로그는 두 query만 포함하므로 실제 Q 검증 증거가 아니다.
