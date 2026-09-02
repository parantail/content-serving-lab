package e1phaseb

import (
	"testing"
	"time"

	"github.com/parantail/content-serving-lab/internal/media"
)

func TestBuildSchedulePreservesScenarioCountsAndAlternatesPairs(t *testing.T) {
	t.Parallel()

	schedule := buildSchedule(2)
	if len(schedule) != 14 {
		t.Fatalf("schedule length = %d, want 14", len(schedule))
	}
	counts := make(map[string]int)
	for _, item := range schedule {
		counts[item.ID]++
	}
	for _, scenario := range expectedScenarios() {
		if counts[scenario] != 2 {
			t.Fatalf("scenario %s count = %d, want 2", scenario, counts[scenario])
		}
	}
	if schedule[0].ID != ScenarioS3ColdControl || schedule[7].ID != ScenarioS3Cold {
		t.Fatalf("cold pair did not alternate: %s then %s", schedule[0].ID, schedule[7].ID)
	}
	if schedule[5].ID != ScenarioS42 || schedule[12].ID != ScenarioS44 {
		t.Fatalf("S4 pair did not alternate: %s then %s", schedule[5].ID, schedule[12].ID)
	}
}

func TestCancellationTrialKeepsSharedWorkAlive(t *testing.T) {
	config := Config{
		RunID: "test-f2", RequestTimeout: 2 * time.Second, TransformTimeout: time.Second,
		ResourceSampleGap: 10 * time.Millisecond, TransformConcurrency: media.DefaultTransformConcurrency,
	}
	item := scenario{ID: ScenarioF2, Concurrency: 10, HotRequests: 10, Cancellation: true, Repetition: 1}
	trial, requests, _, metrics, events, err := executeCancellationTrial(config, item, []byte("fixture"), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "test-host")
	if err != nil {
		t.Fatal(err)
	}
	if !trial.Valid {
		t.Fatalf("trial invalid: %s", trial.InvalidReason)
	}
	if len(requests) != 11 || len(metrics) != 1 || len(events) != 6 {
		t.Fatalf("raw lengths = requests %d, metrics %d, events %d", len(requests), len(metrics), len(events))
	}
	if requests[0].ErrorType != "canceled" {
		t.Fatalf("leader error = %q, want canceled", requests[0].ErrorType)
	}
	for _, request := range requests[1:10] {
		if request.HTTPStatus != 200 || request.ErrorType != "" || !request.Coalesced {
			t.Fatalf("waiter result = status %d error %q coalesced %t", request.HTTPStatus, request.ErrorType, request.Coalesced)
		}
	}
	if requests[10].Cache != "derivative" {
		t.Fatalf("follow-up cache = %q, want derivative", requests[10].Cache)
	}
}

func TestParsePrometheusKeepsIntegerCounters(t *testing.T) {
	t.Parallel()

	values := parsePrometheus([]byte("media_transform_inflight 0\nmedia_transform_attempts_total{result=\"success\"} 4\nmedia_transform_duration_seconds_sum 1.5\n"))
	if values["media_transform_inflight"] != 0 || values[`media_transform_attempts_total{result="success"}`] != 4 {
		t.Fatalf("parsed values = %#v", values)
	}
	if _, ok := values["media_transform_duration_seconds_sum"]; ok {
		t.Fatal("floating-point metric was parsed as an integer counter")
	}
}
