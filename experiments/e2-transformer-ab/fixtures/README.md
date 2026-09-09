# E2 corpus

24개 정상 입력과 72개 독립 geometry reference, 3개 invalid 입력을 고정한다. 파일별 크기·SHA-256·orientation·alpha·품질 평가 대상 여부는 [manifest](generated/manifest.json)에 있다. 측정 전 hash가 다르면 실행하지 않는다.

## 원본과 사용 조건

| 원본 | 출처·저작자 | 라이선스 | 원본 SHA-256 |
| --- | --- | --- | --- |
| 풍경 4928×3264 JPEG | [기존 E1 fixture와 출처](../../e1-cache-stampede/fixtures/README.md), www.Pixel.la Free Stock Photos | CC0 1.0 | `de206136ed0eeaa42499840f49fa35bd3d57745b8a9580b16f0d6e6393ac7f91` |
| 과일 5040×3234 JPEG | [Bowl of Fruit](https://commons.wikimedia.org/wiki/File:Bowl_of_Fruit_-_50838360301.jpg), Alabama Extension / Margaret Barse | [CC0 1.0](https://creativecommons.org/publicdomain/zero/1.0/) | `8f0186504ef5a225272692c7e3bfaf725a33f8bf7e4813eab37ce8d606bb1000` |

과일 원본은 [Wikimedia 원본](https://upload.wikimedia.org/wikipedia/commons/3/3c/Bowl_of_Fruit_-_50838360301.jpg)에서 2026-09-09 다운로드했다. 원본은 7,526,606 bytes이며 Commons 파일 설명의 CC0와 2021-03-30 Flickr 검토 기록을 확인했다. 사진 출처의 라이선스와 사이트 전체 metadata 라이선스를 구분한다.

Alpha·orientation 패턴은 이 저장소의 [생성기](../corpus.py)가 만든 도형이며 CC0 1.0으로 제공한다. 사람 얼굴, 로고, 외부 폰트 또는 생성형 이미지 모델을 사용하지 않는다.

## 고정 입력

- 사진 2종 × 장변 1024/4096 × JPEG/PNG/WebP/AVIF = 16개. 원본을 확대하지 않는다.
- Alpha 도형 × 1024×768/4096×3072 × PNG/WebP/AVIF = 6개.
- 회전 패턴은 저장 픽셀 768×1024와 EXIF 6/8을 조합해 모두 upright 1024×768이 되는 JPEG 2개다.
- 품질 대표 8개: `landscape-small-jpeg`, `landscape-large-webp`, `fruit-small-png`, `fruit-large-avif`, `alpha-small-png`, `alpha-large-webp`, `alpha-small-avif`, `orientation-6-jpeg`.
- Invalid는 100-byte truncated JPEG, 일반 text, IHDR가 100000×100000인 PNG다. 마지막 파일은 공통 크기 검증에서 decode 전에 거부해야 한다.

JPEG Q95/4:4:4, WebP Q90/method 4, PNG compression 6, AVIF libavif+aom Q90/alpha Q100/4:4:4/8-bit/speed 8/jobs 1로 생성한다. 이들은 입력 생성 옵션이며 측정 출력의 Q80과 다르다. 정확한 tool/native 패키지는 manifest에 기록한다.

ICC가 있으면 Pillow/LCMS로 sRGB를 만든다. Reference는 **각 실제 입력을 decode한 픽셀**에서 Pillow Lanczos로 생성한 무손실 RGBA다. 입력 포맷 생성 때 생긴 손실을 출력 변환기의 손실로 혼동하지 않는다. JPEG/PNG/WebP는 Pillow로, AVIF는 libavif `avifdec`로 읽는다. 비교 대상의 변환 출력은 reference 생성에 사용하지 않는다. Alpha resize는 Pillow의 premultiplied RGBA 경로를 사용한다.

Geometry는 장변 640 resize, 640×480 contain, 중앙 640×640 cover다. 중간 크기는 양수 round-half-up이고 cover 좌표는 남은 폭/높이의 floor-half다. Metadata는 reference에 넣지 않는다.

이 corpus는 두 실제 사진과 합성 패턴의 포맷 변형이다. 서로 다른 사진 24장이나 production traffic 표본으로 표현하지 않는다.

## 재현과 검증

공개 저장소 root에서 PowerShell 7로 실행한다. 이미지 binary는 Git LFS로 관리한다.

```powershell
git lfs pull
docker build -f experiments/e2-transformer-ab/Dockerfile.tools -t e2-tools:local .
docker run --rm --network none --cpus=2 --memory=2g --mount "type=bind,source=$((Get-Location).Path),target=/repo" e2-tools:local experiments/e2-transformer-ab/corpus.py --verify
docker run --rm --network none --cpus=2 --memory=2g --mount "type=bind,source=$((Get-Location).Path),target=/repo" e2-tools:local experiments/e2-transformer-ab/corpus.py --output /repo/dist/e2-corpus-repeat
```

생성기는 기존 출력 폴더를 덮어쓰지 않는다. 검증은 24개 입력의 hash/실제 해상도/alpha, 8개 대표 입력, 72개 reference hash와 독립 재계산 픽셀, invalid hash를 확인한다. 다른 출력 폴더에 재생성한 파일의 SHA-256을 원본 manifest와 대조한다. 도구 환경이 바뀌어 일치하지 않으면 새 corpus로 덮어쓰지 않는다.
