# E1 vertical slice calibration

기준일: 2026-09-02
상태: **Retained measurement 조건 확정**

Calibration은 workload와 resource contract를 검증하기 위한 실행이며 대표 성능 결과에서 제외합니다. 아래 값은 모두 Docker Desktop의 Linux `amd64` container에서 fixture `de206136...7f91`, libvips 8.16.1, 1 vCPU 조건으로 측정했습니다.

## 확인 과정

| Run | 바꾼 조건 | 관측 | 처리 |
| --- | --- | --- | --- |
| `calibration-20260902-b` | 4 GiB, native transform 건수 상한 없음, request 30초 | S1-50/100이 memory limit에 닿고 timeout; S1-100은 100건 중 62건만 transform 시작 | 제외. libvips 내부 concurrency와 service 작업 상한을 분리 |
| `calibration-20260902-d` | 2 GiB, transform 상한 4, request/transform 90초/60초 | 15/15 trial 성공; 연속 trial 사이 native allocator의 RSS 잔류 발견 | 제외. trial마다 별도 자식 프로세스 사용 |
| `calibration-20260902-e` | 자식 프로세스 격리, start-skew 5ms | S1/S2-100의 skew 17.4–31.0ms; 요청·출력·counter는 일치 | 제외. 1 vCPU loopback generator 분포 재측정 |
| `calibration-20260902-f` | start-skew 50ms | 100-way skew 최대 69.31ms, S2는 매회 99 waiter coalesced | 제외. 관측 최대값에 여유를 둠 |
| `calibration-20260902-g` | start-skew 100ms | 8/8 trial 유효, raw 교차 검증 통과; 최대 skew 38.81ms | 최종 조건 확인, 성능 결과에서는 제외 |

`calibration-20260902-g`에서 S1-100은 100회 변환을 끝냈고 peak cgroup memory는 1,388,523,520 bytes였습니다. S2-100은 실제 변환 1회, coalesced waiter 99개, peak cgroup memory 166,100,992 bytes였습니다. 이 수치는 조건을 고정하는 근거일 뿐 10회 retained result를 대신하지 않습니다.

## 확정값

- Container: 1 vCPU, 2 GiB
- 동시에 실행할 native transform: 4
- libvips concurrency: 1, operation cache disabled
- Request timeout: 90초
- Shared transform timeout: 60초
- Start-skew limit: 100ms
- Resource sample interval: 10ms
- Trial isolation: scenario/repetition마다 예열된 새 자식 프로세스

100ms는 같은 1 vCPU를 server와 공유하는 현재 loopback generator에만 적용합니다. 외부 generator나 다른 CPU quota에서 실행하면 retained result와 섞지 않고 다시 calibration합니다.
