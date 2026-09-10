# Local verification and calibration — 2026-09-10

**576/576개 출력 검증과 warm-up 포함 2,304/2,304회 calibration이 성공했다.** 이 자료는 개발 단계의 기능·실행 가능성 확인이며 5회 반복 본 측정이나 AWS 성능 결과가 아니다.

최종 runtime 개발 이미지에서는 같은 Task의 사전 진단 16회와 quality sweep 192회도 성공했다. 192개 출력을 독립 디코딩했고 curve의 Q80 48개는 앞선 validation과 hash/bytes가 전부 같았다. [Task 내 encoder 설정](quality/preflight/settings.json), [품질 분석](quality-analysis/quality.json), [Q80 hash 대조](quality-hash-check.json)를 보존했다. 이 run에서 사전 진단은 libvips threads 1/ImageMagick 28, speed 5를 확인했고 본 quality worker에는 probe를 넣지 않았다.

| 확인 | 결과 |
| --- | --- |
| Native 호출 | Go govips/libvips와 imagick/MagickWand, 동일 encoded corpus |
| 자원 | Docker Linux amd64, 각 run 1 vCPU·2 GiB, cgroup v2, non-root |
| 출력 검증 | 24입력 × 3 geometry × 4출력 × 2 engine = 576개 독립 디코딩 성공 |
| 정책 | geometry, alpha 보존/JPEG white flatten, JPEG 4:4:4, PNG 8-bit truecolor 확인 |
| 시각 확인 | EXIF 6/8 방향 패턴, 과일 질감·alpha 경계의 cover 확대 crop 확인 |
| Calibration | 48 batch, 성공 측정 1,152 + warm-up 1,152, 오류·timeout·OOM 없음 |
| Calibration batch 실행 wall | 571.897초 |
| 5회 예상 batch 실행 시간 | 2,859.486초 = 47.658분 |
| 25% 여유 적용 | 3,574.357초 = 59.573분, 60분 gate 통과 |
| Worker lifetime RSS 최대 | libvips 1,025.629 MiB / ImageMagick 999.965 MiB |
| Container cgroup sampled peak 최대 | libvips batch 1,006.355 MiB / ImageMagick batch 954.965 MiB |
| RSS 교차 확인 | 모든 batch의 worker self HWM와 개별 wait4 HWM 일치 |
| Cgroup scope | parent + worker 직접 구성원 2개, hidden member 0, CPU/memory 제한 확인 |
| 출력 일관성 | Calibration 측정 1,152개 hash/bytes가 독립 디코딩한 Q80 validation과 전부 일치 |
| 독립 재분석 | 별도 실행에서 validation 5개 산출물·calibration 4개 산출물 SHA-256 동일 |

Calibration container의 시작/종료 시각과 image/binary hash는 [environment.json](environment.json)에 있다. Parent 최초 corpus hash 검증·package/manifest 준비 약 40초는 571.897초의 batch loop 앞에 있다. 인프라/Task 총시간에는 초기화도 포함한다. 처리량/latency는 transform 구간, batch 처리량은 24개 측정 pass의 wall time이다. RSS와 cgroup sampled peak는 서로 다른 값이다.

Native adapter/worker 파일은 공개 `25742ebaec01193fb8aee07f4d8e479fc24f0d82`와 같다. Supervisor/analyzer는 첫 구현 checkpoint 전 개발 build이며 manifest의 `commit=development`를 사후 변경하지 않았다. 최종 supervisor는 여기에 worker thread 수 표본과 같은 Task 안의 사전 codec 진단을 추가한다. 개발 이미지의 시간 수치를 최종 AWS 이미지의 본 측정으로 승격하지 않는다.

로컬 AVIF 설정은 [읽기 전용 probe](../../diagnostics/README.md)의 libvips threads 1 / ImageMagick threads 28을 따르는 표준 패키지 비교다. 이 값은 동시에 소비한 CPU 수가 아니다. AWS 동일 이미지에서 실제 설정·계측 scope와 calibration을 다시 확인해야 한다.

원자료와 분석:

- [Validation manifest](validate/manifest.json), [완료 상태](validate/completion.json), `validate/outputs/`의 576개 encoded 출력.
- [Validation 분석](validate-analysis/verification.json), [품질 수치](validate-analysis/quality.json), [확대 crop](validate-analysis/crops.png).
- [Calibration manifest](calibrate/manifest.json), [완료 상태](calibrate/completion.json), [batch 집계](calibrate-analysis/batches.json).
- 각 run의 batch JSON/JSONL/cgroup 표본/stderr를 보존했다. 입력별 시간은 calibration 한 관측뿐이므로 우열이나 p95 일반화 근거로 쓰지 않는다.

공개 저장소 root에서 [RUN.md](../../RUN.md)의 tools 이미지로 재생성한다.

```powershell
docker run --rm --network none --cpus=2 --memory=2g --mount "type=bind,source=$((Get-Location).Path),target=/repo" e2-tools:local experiments/e2-transformer-ab/analyze.py experiments/e2-transformer-ab/results-local/preflight-20260910/validate dist/e2-validation-reanalysis
docker run --rm --network none --cpus=2 --memory=2g --mount "type=bind,source=$((Get-Location).Path),target=/repo" e2-tools:local experiments/e2-transformer-ab/analyze.py experiments/e2-transformer-ab/results-local/preflight-20260910/calibrate dist/e2-calibration-reanalysis
```

독립 분석은 CPU 2개·2 GiB의 별도 tools container에서 수행했다. 이는 분석 도구의 자원 설정이며 native 비교의 1 vCPU 조건과 섞지 않는다. Figure generator의 본 측정용 세 그래프는 아직 이 자료로 생성하지 않았다.
