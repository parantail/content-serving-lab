package e1runner

import "testing"

func TestBuildScheduleAlternatesModesAndPreservesTrialCounts(t *testing.T) {
	t.Parallel()

	schedule := buildSchedule(2)
	if len(schedule) != 15 {
		t.Fatalf("schedule length = %d, want 15", len(schedule))
	}
	if schedule[0].ID != "S0" || schedule[7].ID != "S0" || schedule[len(schedule)-1].ID != "F1" {
		t.Fatalf("unexpected schedule boundaries: first=%s second-repeat=%s last=%s", schedule[0].ID, schedule[7].ID, schedule[len(schedule)-1].ID)
	}
	if schedule[1].Mode == schedule[8].Mode {
		t.Fatalf("first 10-request mode did not alternate: %s, %s", schedule[1].Mode, schedule[8].Mode)
	}
}

func TestPercentileUsesSamplesWithinOneTrial(t *testing.T) {
	t.Parallel()

	samples := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if got := percentile(samples, 0.50); got != 5 {
		t.Fatalf("p50 = %v, want 5", got)
	}
	if got := percentile(samples, 0.95); got != 9 {
		t.Fatalf("p95 = %v, want 9", got)
	}
	if got := percentile(samples, 0.99); got != 9 {
		t.Fatalf("p99 = %v, want 9", got)
	}
}
