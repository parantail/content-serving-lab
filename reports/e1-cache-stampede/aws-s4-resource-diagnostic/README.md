# Fargate 자원 계측 진단

2026-09-08, commit `0d0beb9949b1c4f17f683341b7a7c431cffe2eea`의 별도 one-shot 진단이다. 이미지 요청 성능의 retained 결과가 아니다. Fargate 1.4.0·1 vCPU·2GiB·cgroup v1에서 Media Service와 동일 task definition/image/user로 수행했다. 실행 전후 범위 4행, 무부하/CPU 부하 × sampler off/on 각 3회 12행, 완료 1행을 전부 보존했다. 반복 2는 실행 순서를 반대로 했다.

- [진단 원자료](diagnostic.jsonl), [실행 환경과 image digest](execution.json). 회수 사본에서 줄바꿈만 정규화했으며 JSON 값과 순서는 바꾸지 않았다.
- CPU 전후 별칭 해석·mount 대응·membership/mount-root 일치가 true다. Memory도 mount·membership이 대응한다. 두 counter 모두 self 직접 멤버 1개, 숨겨진 멤버/하위 group 0개를 관측했다. 관측 시점의 container-visible 범위이며 모든 Fargate Task 구성에 대한 보장은 아니다.
- CPU 부하에서 cgroup과 process user+system CPU 차이 최댓값은 **0.223213ms**다. 이는 이번 교차검증 관측값이며 오차 상한이나 정확도 보증이 아니다.

각 구간은 약 1초이고 sampling은 50ms다.

| 반복 | Idle off CPU ms | Idle on CPU ms | 추가 CPU ms | Busy 처리량 on/off 차이 |
| --- | ---: | ---: | ---: | ---: |
| 1 | 0.311434 | 7.817420 | 7.505986 | -1.46% |
| 2 | 0.239330 | 6.037175 | 5.797845 | -2.68% |
| 3 | 0.392906 | 7.719188 | 7.326282 | -0.67% |

추가 CPU = idle on − off. Busy 처리량 차이 = `100 × ((on.iterations/on.wall_ms)/(off.iterations/off.wall_ms) − 1)`. 작은 표본의 합성 workload 관측이며 순수 sampler 인과효과나 실제 이미지 응답 지연 변화로 일반화하지 않는다. CPU에는 sampler 비용도 포함하고 사후 차감하지 않는다.

이 근거로 50ms sampling을 유지하되, 정확한 순간 memory peak나 수% 차이의 원인을 확정하는 도구로 사용하지 않는다. 새로운 image/runtime/resource 설정이면 범위를 다시 확인한다. 실행 절차와 해석 계약은 [계측 계약](../../../experiments/e1-cache-stampede/AWS-S4-MEASUREMENT.md)을 따른다.

실행 후 Terraform 71개 삭제와 독립 잔여 실행 자원 검사 통과를 확인했다. 계정 식별자·실제 ARN·원본 mount 경로·credential은 포함하지 않는다.
