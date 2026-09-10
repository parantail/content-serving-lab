# E2 AVIF quality 전달 수정 — 로컬 검증과 인증 대기

ImageMagick AVIF가 요청 Q 대신 기본 Q50을 쓰던 adapter를 수정했다. `SetImageCompressionQuality`에 더해 AVIF writer가 읽는 ImageInfo의 `SetCompressionQuality`를 설정한다. [이전 AWS 결과](../aws-20260910/README.md)는 변경하지 않으며 유효 비교로 사용하지 않는다.

수정본 runtime의 실제 AV1 encoder를 조회해 두 engine × Q50/65/80/90 × 대표 입력 8개, **64회 모두 요청 quality와 실제 값이 일치**함을 확인했다. 모든 query 오류는 0이고, 두 engine의 8개 입력 모두 네 Q의 출력 hash가 각각 달랐다. 동일 chroma의 Q50/Q80 출력 차이를 확인하는 native 회귀 테스트는 이전 adapter에서 ImageMagick만 실패하고 수정본에서 통과했다.

- 환경: 로컬 Docker Linux amd64, 1 vCPU·2 GiB, 동일 corpus와 표준 Debian codec. AOM 3.12.1 speed 5, 실제 encoder threads libvips 1/ImageMagick 28.
- 검증: `go test ./...`, `go vet ./...`, `go test -tags=e2integration ./internal/e2` 통과. Python 9개 검사와 PowerShell 구문·실행 확인 통과.
- 새 실행 gate: diagnose 및 각 Task preflight의 16개 AVIF 호출에서 실제 Q80과 query 성공을 요구한다. Analyzer도 새 contract의 원본 trace와 settings를 독립 확인한다. 기존 AWS matrix 호출 수 14,672는 유지한다.
- 중단: AWS 인증 확인에서 로그인 만료가 발생했다. 수동 대응이 필요한 경우 중단하는 실행 조건에 따라 진행 중인 로컬 validation을 SIGTERM으로 종료했다. 24개 batch 중 7개 완료, 8번째 중단, completion은 `valid=false`, `error=context canceled`다. 전체 validation·calibration·새 AWS 실행은 미완료다. 이는 timeout이나 수정본 품질 오류로 판정한 결과가 아니다.
- 자원: 이번 확인에서 AWS 자원을 생성하지 않았다. 중단한 로컬 container의 원자료를 회수했다. 기존 AWS 배포의 제거 근거는 이전 보고서에 있다.

[검증 기록](verification.json), [64회 원자료](quality-sweep/verification.json), [중단한 validation](interrupted-validation/completion.json), [same-container 실제 Q80 설정](interrupted-validation/preflight/settings.json), [원자료 SHA-256](sha256.json)을 보존한다. Runtime digest는 `sha256:48b9eb87137d36f3879740c7dd6c690321d75857d1c9d6fecdb202ed54db33ee`다. 이 이미지는 base `fb5e51f` 위 수정본의 development build이며 clean commit build로 표현하지 않는다. 정확한 compiled source 파일 hash를 검증 기록에 남겼다.

재현은 공개 저장소 root에서 [RUN.md](../../../experiments/e2-transformer-ab/RUN.md)의 build·test 명령 후 다음을 실행한다.

```powershell
./experiments/e2-transformer-ab/diagnostics/quality-sweep.ps1 -Image e2:local -RunId <fresh-quality-probe-id>
docker run --name <fresh-container-name> --network none --cpus=1 --memory=2g e2:local -mode validate -output /results/<fresh-validation-id>
```

인증 복구 후 새 이름으로 전체 local validation·calibration을 수행한다. 30초 native 제한과 210분 measurement gate를 통과하면 clean checkpoint로 AWS diagnose/validate/calibrate/measure/quality를 새로 실행하고 회수·제거한다. 로컬 진단의 timing을 AWS 성능에 합치지 않으며 아직 변환기를 선택하지 않는다.
