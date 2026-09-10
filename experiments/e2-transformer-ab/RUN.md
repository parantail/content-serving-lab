# E2 로컬 실행과 분석

공개 저장소 root에서 실행한다. [기술 계약](README.md)과 [AWS workflow](../../deploy/e2-transformer-ab/README.md)는 동일 matrix를 사용한다. 모든 run/output directory는 새 이름이어야 한다.

```powershell
docker build -f Dockerfile.e2 --target dev -t e2-dev:local .
docker run --rm --network none --mount "type=bind,source=$((Get-Location).Path),target=/src" -w /src e2-dev:local sh -c 'go test ./... && go vet ./... && go test -tags=e2integration ./internal/e2'
$commit = git rev-parse HEAD
docker build --platform linux/amd64 --provenance=false -f Dockerfile.e2 --target runtime --build-arg "GIT_COMMIT=$commit" -t e2:local .
docker run --name e2-validation --network none --cpus=1 --memory=2g e2:local -mode validate -output /results/local-validate
docker cp e2-validation:/results/local-validate dist/
docker run --name e2-calibration --network none --cpus=1 --memory=2g e2:local -mode calibrate -output /results/local-calibrate
docker cp e2-calibration:/results/local-calibrate dist/
```

container 안의 `/results`에 기록하고 종료 후 복사한다. Windows bind mount의 소유권 차이를 피하고 성능 측정 중 호스트 filesystem 쓰기를 넣지 않는다. 복사한 원자료를 검증한 후 정지한 container를 `docker rm e2-validation e2-calibration`으로 제거한다.

```powershell
docker build -f experiments/e2-transformer-ab/Dockerfile.tools -t e2-tools:local .
docker run --rm --network none --cpus=2 --memory=2g --mount "type=bind,source=$((Get-Location).Path),target=/repo" e2-tools:local experiments/e2-transformer-ab/analyze.py dist/local-validate dist/local-validate-analysis
docker run --rm --network none --cpus=2 --memory=2g --mount "type=bind,source=$((Get-Location).Path),target=/repo" e2-tools:local experiments/e2-transformer-ab/analyze.py dist/local-calibrate dist/local-calibrate-analysis
$calibration = Get-Content dist/local-calibrate/completion.json -Raw | ConvertFrom-Json
if (-not $calibration.valid -or $calibration.wall_ns / 1e9 * 5 * 1.25 -gt 3600) { throw 'Calibration gate failed; do not measure.' }
```

AWS 회수 디렉터리에도 같은 analyzer를 사용한다. `validate`는 두 adapter의 모든 Q80 출력 576개를 독립 디코딩하여 geometry·alpha·JPEG 4:4:4·PNG truecolor와 reference/hash를 확인한다. `quality`는 대표 8개·4 quality·3 lossy format·두 engine의 192개 출력이다. `measure`에는 실제 출력 파일 저장을 끄고 hash/bytes만 남겨 파일 쓰기를 batch wall time에 넣지 않는다.

`check_outputs.py <validation-analysis> <measurement-analysis> <new-output.json>`으로 동일 cohort의 측정 output hash/bytes를 독립 디코딩한 Q80 validation과 대조한다. 값이 다르면 오류로 남기고 검증한 품질을 해당 측정에 그대로 적용하지 않는다. Quality 분석을 두 번째 인자로 주면 curve 중 Q80 관측점을 같은 방식으로 확인한다.

```powershell
# 동일 AWS cohort의 analyze 결과를 넣는다.
docker run --rm --network none --mount "type=bind,source=$((Get-Location).Path),target=/repo" e2-tools:local experiments/e2-transformer-ab/report.py <measurement-analysis> <quality-analysis> <new-report-output>
```

보고서 데이터는 raw batch JSON/JSONL에서 계산한다. 새 output 경로로 재분석 후 JSON과 chart의 SHA-256을 대조하면 독립 재생성을 확인할 수 있다. `crops.png`는 cover Q80의 방향·과일·alpha 확대 예시이며 사람의 시각 확인이 별도로 필요하다.

원자료 형식:

- `manifest.json`: commit·corpus hash·정확한 전체 job·seed·package 목록·cohort·호출 수.
- `<batch>.jsonl`: ready/pass/start/finish/summary. Duration은 native 객체 해제까지이며 output hash 계산과 파일 쓰기는 제외한다. Queue는 pass 시작 후 slot까지다. 성능 batch wall에는 scheduling·hash·프로토콜 계측 overhead가 포함된다.
- `<batch>.json`: 개별 wait4 RSS/CPU와 worker 자체 RSS, cgroup sampled peak/CPU, parent+worker membership, 종료 코드와 오류.
- `<batch>.cgroup.jsonl`: 50 ms 표본과 경계 표본. Worker lifetime의 cgroup 값은 parent/worker·page cache도 포함하므로 RSS와 같지 않다.
- `<batch>.stderr.log`: native 진단과 오류. diagnose에서만 LD_PRELOAD로 AVIF encoder threads/speed를 읽는다. 성능 run에는 probe를 주입하지 않는다.
- `completion.json`: 전체 예정/완료 batch와 유효 여부. 실패 시 성공 결과로 분석하지 않고 미시작 job 및 timeout/interrupted를 보존한다.

worker `ru_maxrss`에는 Go runtime, corpus/reference hash 검증의 임시 할당, encoded input preload와 warm-up도 포함된다. 두 engine에 같은 경로를 사용하며 baseline을 빼지 않는다. wait4 값은 종료까지의 개별 child HWM이므로 마지막 summary JSON 할당으로 self 값보다 커질 수 있다. 누적 RUSAGE_CHILDREN은 쓰지 않는다.

Fargate는 mode별 Task를 다른 호스트에 배치할 수 있다. 따라서 diagnose 외의 mode도 동일 Task의 `preflight/`에서 16회 별도 AVIF probe를 먼저 실행하고 `settings.json`에 AOM 버전·threads·speed·scope를 검증한다. 이 worker를 종료한 후 새로운 worker로 본 matrix를 실행하며 성능 worker에는 LD_PRELOAD를 넣지 않는다. Manifest의 `preflight_diagnostic_calls`는 measured/warm-up 호출 수와 별도다. 60분은 batch 실행 loop에 적용하고 최초 corpus/manifest 준비·사전 진단은 별도이며, 전체 Task 시간은 외부 Task supervisor와 2시간 인프라 deadline에도 제한된다.

`GOMAXPROCS=1`, `VIPS_CONCURRENCY=1`, `OMP_NUM_THREADS=1`, `MAGICK_THREAD_LIMIT=1`이다. ImageMagick memory 1,536 MiB/map 0/disk 0/area 20MP와 thread 1을 설정한다. 전체 메모리는 Docker/Fargate 2 GiB로 제한한다. AVIF delegate 기본 thread 수는 별도로 진단하고, worker 전체의 sampled thread peak는 codec thread 설정과 구분한다.

JPEG는 4:4:4·non-progressive·metadata 제거, WebP는 lossy method/effort 4·alpha quality 100, AVIF는 AOM speed 5·8-bit·Q90 미만 4:2:0/Q90 이상 4:4:4다. PNG는 compression 6·8-bit이며 libvips는 RGB/RGBA를 선택하고 ImageMagick은 RGBA/color-type 6·filter 0을 지정한다. AVIF tiling 등 양쪽 API가 노출하지 않는 delegate 기본값까지 동일하다고 주장하지 않는다. 정확한 패키지는 각 manifest에 보존한다. Worker 전체 thread 수에는 Go runtime과 decoder 등의 thread도 포함되며 encoder threads 설정값과 같지 않다.

E1 연결 검증 `TestE1JPEGToWebPConnection`은 고정 E1 JPEG를 기존 image-thumbnail과 E2 buffer-thumbnail에서 cover640/WebP Q80으로 변환하여 Go WebP decoder로 두 크기를 확인한다. 코드 경로·resampler가 달라 byte 동일성을 요구하지 않으며 E1 성능 원자료는 변경하지 않는다.
