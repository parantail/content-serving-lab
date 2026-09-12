# E1 fixture

E1의 첫 비교는 공개 재배포가 가능한 고해상도 JPEG 한 장을 고정 입력으로 사용합니다. Binary image는 Git LFS로 관리합니다.

## `landscape-4928x3264.jpg`

| 항목 | 값 |
| --- | --- |
| 설명 | 산과 하늘이 포함된 자연 풍경 사진 |
| 저작자 credit | www.Pixel.la Free Stock Photos |
| 출처 페이지 | [Wikimedia Commons](https://commons.wikimedia.org/wiki/File%3ALandscape-mountains-nature-hiking_%2824300577396%29.jpg) |
| 원본 파일 | [Wikimedia 원본 JPEG](https://upload.wikimedia.org/wikipedia/commons/0/09/Landscape-mountains-nature-hiking_%2824300577396%29.jpg) |
| 라이선스 | [CC0 1.0 Universal](https://creativecommons.org/publicdomain/zero/1.0/) |
| 다운로드 날짜 | 2026-09-01 |
| 파일 크기 | 4,075,024 bytes |
| 해상도 | 4,928 × 3,264 pixels |
| 포맷 | JPEG, 24-bit RGB, sRGB |
| SHA-256 | `de206136ed0eeaa42499840f49fa35bd3d57745b8a9580b16f0d6e6393ac7f91` |

Wikimedia Commons의 파일 페이지는 이 사진이 Flickr에서 가져온 `www.Pixel.la Free Stock Photos`의 작업이며 FlickreviewR가 CC0 상태를 확인했다고 기록합니다. CC0는 attribution을 요구하지 않지만 원본을 추적할 수 있도록 저작자와 출처를 함께 남깁니다.

약 16.1 megapixel인 이 원본은 고해상도 JPEG decode와 큰 폭의 축소 작업을 만들면서도 공개 저장소의 첫 fixture로 관리 가능한 크기라서 선택했습니다. 첫 E1 조건은 이 입력을 `640 × 640`, `cover`, WebP quality 80으로 변환합니다. `640 × 640`은 원본 크기가 아니라 card/thumbnail용 파생 이미지 크기입니다. 같은 원본의 `1280 × 1280` 변환 민감도 비교는 이번 E1에서 실행하지 않았습니다.

## 받기와 확인

Git LFS가 설치된 환경에서 fixture를 받습니다.

```bash
git lfs install
git lfs pull
sha256sum experiments/e1-cache-stampede/fixtures/landscape-4928x3264.jpg
```

출력된 SHA-256이 위 표와 다르면 실험에 사용하지 않습니다.
