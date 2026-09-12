# README 요약 그래프

저장소 첫 화면에 넣는 실험별 요약 그래프입니다. 세 파일 모두 보고서가 참조하는 retained 원자료의 분석 산출물에서 `cmd/readme-figures`가 그립니다. 손으로 그리거나 수치를 옮겨 적은 그래프는 없습니다.

| 파일 | 보여주는 것 | 입력 |
| --- | --- | --- |
| `e1-duplicate-transforms.svg` | 같은 이미지의 동시 cold 요청 100개당 실제 변환 횟수. 로컬 단일 프로세스의 `none`/`process-singleflight`와 AWS Task 1/2/4개 | [Phase A `summary.csv`](../../experiments/e1-cache-stampede/results/retained-20260902-68135d3ad33f/analysis-retained/summary.csv), [AWS S4 `summary.csv`](../../experiments/e1-cache-stampede/results-aws-s4/retained-ad80a58dee07/analysis/summary.csv)와 각 `run.json` |
| `e2-throughput-ratio.svg` | 포맷별 libvips ÷ ImageMagick 처리량 비율. 조건 24개(포맷 4 × geometry 3 × 동시성 2)의 다섯 반복 중앙값 비율과 RSS가 더 높았던 조건 수 | [`conditions.json`](../../reports/e2-transformer-ab/aws-20260910-c2/figures/conditions.json) |
| `e3-blast-radius.svg` | T2 변환 정지 장애 중 정상 miss 요청의 5초 구간 p99 timeline (M0 baseline, M1 bounded wait)과 hit stream | [`timeline-summary.csv`, `blast-radius.csv`, `intervals.csv`](../../experiments/e3-failure-isolation/results/retained-e8c21674a86f5d4737b23a59e2071da7754f4ff2/analysis/)와 `run.json` |

## 다시 만들기

```bash
go run ./cmd/readme-figures
go test ./internal/readmefigures
```

`TestCommittedFiguresAreCurrent`는 입력 파일에서 다시 그린 SVG가 이 디렉터리의 파일과 byte 단위로 같은지 확인합니다. 원자료나 그리는 코드를 바꾸면 명령을 다시 실행해 갱신합니다. 생성기는 cgo 없이 동작하므로 libvips가 없는 환경에서도 실행할 수 있습니다.

## 그리는 규칙

- 색은 세 가지만 씁니다. 주황은 변경 전(baseline, `none`), 파랑은 채택한 방식, 초록은 맥락용 stream(캐시 적중 요청)입니다. 색만으로 계열을 구분하지 않도록 범례와 직접 label을 함께 둡니다.
- 제목은 그래프가 답하는 질문, 부제는 환경·입력·반복 횟수·집계 방식입니다.
- 축, 단위, 장애 구간과 반복 횟수를 그래프 안에 적습니다.
