# E3 calibration 기록 (2026-09-11)

실행 조건을 정하기 위한 calibration 네 번의 요약이다. 대표 결과가 아니며 `--calibration`으로 실행해 `run.json`의 `calibration`이 `true`다. 용량 때문에 요청별 원자료(`requests.csv`, `resources.csv`)는 보존하지 않았고 `trials.csv`, `state.csv`, `logs.jsonl`, `metrics.prom`과 당시 분석 산출물만 남긴다. 따라서 이 디렉터리는 `analyze`로 다시 분석할 수 없다. 본 측정은 원자료 전체를 보존한다.

| 실행 | 조건 | 확인한 것 |
| --- | --- | --- |
| `calib-a-draft-rates` | T0 M0, rate 20/1/2 req/s | CPU 144초/150초로 포화, 정상 miss p50 약 1초, slot 대기 최대 6 → 초안 rate 기각 |
| `calib-b-half-rates` | T0 M0, rate 20/0.5/1 req/s | CPU 약 46%, 대기 0, 정상 miss p50 약 0.55초 → rate 확정 |
| `calib-c-modes` | M0/M1/M2 × T0/T2, 1회 | 세 모드의 차이, 복구 시간, hit 무영향 확인 |
| `calib-d-faults` | M0/M1/M2 × T1/T3/T4, 1회 | 나머지 장애의 주입·유효성 확인, M1의 T4 상충 확인 |

`calib-a`~`calib-d`의 분석은 기준 지연을 "정상 구간 p99 중앙값 × 3"으로 계산한 당시 분석기 버전으로 만들었다. 이후 분석기는 "정상 구간 p99 최댓값 × 3"을 사용하며, `calib-c`만 새 규칙으로 다시 분석한 산출물이다. 두 규칙의 차이는 hit stream의 CPU 경합 지연이 영향으로 분류되는지 여부이고 정상 miss stream의 표에는 거의 영향을 주지 않는다.
