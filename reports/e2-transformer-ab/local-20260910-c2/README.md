# E2 quality 수정본 — 전체 로컬 검증 통과

ImageMagick AVIF quality 전달을 수정한 clean commit `5774b94a2f84d95b12534d5de22057b4307e1bde`에서 전체 validation·calibration·quality가 통과했다. **로컬 총 3,120회 호출 성공, 오류·timeout·interrupted 0**이다. AWS 전체 재측정과 최종 변환기 선택은 아직 완료하지 않았다.

| Mode | 측정 / warm-up / 사전 진단 호출 | Loop 시간 | 최대 native 시간 | 최대 worker wait4 RSS |
| --- | ---: | ---: | ---: | ---: |
| validate | 576 / 0 / 16 | 148.902초 | 1.193초 | 447.83 MiB |
| calibrate | 1,152 / 1,152 / 16 | 600.479초 | 5.621초 | 1,006.64 MiB |
| quality | 192 / 0 / 16 | 58.584초 | 1.402초 | 355.77 MiB |

각 mode는 새 container에서 1 vCPU·2 GiB 제한으로 실행했다. 세 mode의 102개 worker 모두 cgroup membership 일치와 `wait4 RSS >= self RSS`를 확인했다. RSS는 worker 수명 전체의 high-water mark이며 cgroup 사용량과 구분한다. Loop 시간은 최초 corpus 준비와 별도 preflight를 제외한다. 정확한 package·corpus hash·job·시각은 각 raw manifest에 있다.

Calibration 600.478728938초 × 5 × 1.25 = **62.550분**, 승인된 210분 gate 이내다. 로컬과 AWS 속도를 같다고 가정하지 않으므로 AWS에서도 새 calibration을 통과해야 5회 본 측정을 시작한다. Native 30초·인프라 5시간·견적 US$1.50·상한 US$3 조건은 유지한다.

Validation 576개와 quality 192개 출력을 독립 디코딩해 geometry·alpha·JPEG 4:4:4·PNG truecolor 조건 및 SSIM/PSNR을 확인했다. Calibration Q80 출력 1,152개와 quality의 Q80 출력 48개 모두 validation의 hash/bytes와 일치했다. 두 engine·세 lossy format·대표 입력 8개 모두 Q50/65/80/90의 네 출력 hash가 서로 달랐다. 같은 Q 숫자가 같은 화질을 뜻하지는 않는다.

세 container의 preflight 48회 모두 실제 encoder Q80, speed 5, query 오류 0을 확인했다. Encoder threads는 libvips 1/ImageMagick 28이며 표준 Debian delegate 예외를 유지한다. [이전 64회 실제 Q sweep](../quality-fix-20260910/README.md)의 development 이미지와 이번 clean 이미지의 두 worker binary 및 probe shared library가 byte 단위로 동일함을 확인했다. 이번 runtime digest는 `sha256:5a20a4cb1ba993716bd138b6ac8945e248174022b3327fe6f830c34136a3217c`다.

대표 crop에서 EXIF 방향·중앙 crop·알파 합성 상태를 시각 확인했다. 이 로컬 자료로 5회 AWS 처리량/RSS 비교나 최종 선택을 대신하지 않는다. [이전 AWS 결과](../aws-20260910/README.md)는 잘못된 AVIF quality 설정의 진단 기록으로 보존한다.

- [검증 요약과 binary hash](verification.json)
- [Calibration 출력 hash 대조](calibrate-hash-check.json), [quality Q80 대조](quality-hash-check.json)
- [Validation 분석](analysis/validate/verification.json), [calibration 분석](analysis/calibrate/verification.json), [quality 분석](analysis/quality/verification.json)
- [Validation 확대 crop](analysis/validate/crops.png), [quality 확대 crop](analysis/quality/crops.png)
- [원자료와 분석 파일 SHA-256](sha256.json), [독립 재생성 대조](reanalysis-check.json)

## 재현

공개 저장소 root에서 [로컬 실행 명령](../../../experiments/e2-transformer-ab/RUN.md)으로 같은 commit의 runtime을 빌드하고 validate/calibrate/quality를 서로 다른 새 이름으로 실행한다. 아래 명령은 보존된 원자료의 재분석이다. 결과 경로는 존재하지 않아야 한다.

```powershell
foreach($mode in @('validate','calibrate','quality')){
    docker run --rm --network none --cpus=2 --memory=2g --mount "type=bind,source=$((Get-Location).Path),target=/repo" e2-tools:local experiments/e2-transformer-ab/analyze.py "reports/e2-transformer-ab/local-20260910-c2/raw/local-$mode-20260910-c2" "dist/c2-reproduce-$mode"
    if($LASTEXITCODE -ne 0){throw "Analysis failed: $mode"}
}
```

보존한 분석 JSON 12개와 crop PNG 2개를 위 출력과 SHA-256으로 대조한다. Native 성능 실행은 순차 수행했고 독립 재분석은 모든 native 실행 종료 후 진행했다. 이번 로컬 실행 중 AWS 자원은 생성하지 않았으며, 종료한 container를 회수·제거했다.
