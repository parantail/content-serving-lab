package e1runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parantail/content-serving-lab/internal/media"
)

func TestAnalyzeValidatesRawFilesAndBuildsCharts(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	output := RunOutput{
		Directory: directory,
		Metadata:  RunMetadata{SchemaVersion: SchemaVersion, RunID: "test-run", Environment: map[string]string{}},
		Trials: []TrialResult{
			{RunID: "test-run", TrialID: "S1-10-r01", ColdStateID: "S1-10-r01", Scenario: "S1-10", Mode: "none", Repetition: 1, Concurrency: 10, Valid: true, TotalRequests: 2, SuccessRequests: 2, TransformAttempts: 2, P50MS: 10, P95MS: 10, P99MS: 10},
			{RunID: "test-run", TrialID: "S2-10-r01", ColdStateID: "S2-10-r01", Scenario: "S2-10", Mode: "process-singleflight", Repetition: 1, Concurrency: 10, Valid: true, TotalRequests: 2, SuccessRequests: 2, TransformAttempts: 1, CoalescedRequests: 1, P50MS: 5, P95MS: 5, P99MS: 5},
		},
		Requests: []RequestResult{
			{RunID: "test-run", TrialID: "S1-10-r01", RequestID: "one", HTTPStatus: 200, LatencyMS: 10},
			{RunID: "test-run", TrialID: "S1-10-r01", RequestID: "two", HTTPStatus: 200, LatencyMS: 20},
			{RunID: "test-run", TrialID: "S2-10-r01", RequestID: "one", HTTPStatus: 200, LatencyMS: 5},
			{RunID: "test-run", TrialID: "S2-10-r01", RequestID: "two", HTTPStatus: 200, LatencyMS: 6, Coalesced: true},
		},
		Metrics: []trialMetrics{
			{TrialID: "S1-10-r01", Scenario: "S1-10", Snapshot: metricsSnapshot(2, 0)},
			{TrialID: "S2-10-r01", Scenario: "S2-10", Snapshot: metricsSnapshot(1, 1)},
		},
	}
	if err := writeRunOutput(output); err != nil {
		t.Fatal(err)
	}

	analysis, err := Analyze(directory)
	if err != nil {
		t.Fatal(err)
	}
	if analysis.ValidTrials != 2 || len(analysis.Summary) != 2 {
		t.Fatalf("analysis = %+v", analysis)
	}
	for _, name := range []string{"analysis.json", "summary.csv", "transform-count.svg", "latency-error.svg"} {
		info, err := os.Stat(filepath.Join(analysis.Directory, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if info.Size() == 0 {
			t.Fatalf("%s is empty", name)
		}
	}
}

func TestAnalyzeRejectsSummaryMismatch(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	output := RunOutput{
		Directory: directory,
		Metadata:  RunMetadata{SchemaVersion: SchemaVersion, RunID: "bad-run", Environment: map[string]string{}},
		Trials:    []TrialResult{{RunID: "bad-run", TrialID: "S0-r01", ColdStateID: "S0-r01", Scenario: "S0", Mode: "none", Repetition: 1, Concurrency: 1, Valid: true, TotalRequests: 2, SuccessRequests: 2, TransformAttempts: 1, P50MS: 1, P95MS: 1, P99MS: 1}},
		Requests:  []RequestResult{{RunID: "bad-run", TrialID: "S0-r01", RequestID: "one", HTTPStatus: 200, LatencyMS: 1}},
		Metrics:   []trialMetrics{{TrialID: "S0-r01", Scenario: "S0", Snapshot: metricsSnapshot(1, 0)}},
	}
	if err := writeRunOutput(output); err != nil {
		t.Fatal(err)
	}
	if _, err := Analyze(directory); err == nil {
		t.Fatal("Analyze succeeded with mismatched request count")
	}
}

func metricsSnapshot(attempts, coalesced int64) media.MetricsSnapshot {
	return media.MetricsSnapshot{TransformSuccess: attempts, Coalesced: coalesced}
}
