package e3runner

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parantail/content-serving-lab/internal/media"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	fixture := filepath.Join(root, "fixture.jpg")
	if err := os.WriteFile(fixture, bytes.Repeat([]byte("fixture-bytes-"), 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	return Config{
		RunID:                "test-run",
		ResultsRoot:          filepath.Join(root, "results"),
		FixturePath:          fixture,
		GitCommit:            "test",
		ContainerImage:       "test-image",
		Command:              []string{"e3-runner", "run", "--test"},
		Calibration:          true,
		Storage:              StorageLocal,
		Modes:                []media.IsolationMode{media.IsolationBaseline, media.IsolationBoundedWait, media.IsolationKillSwitch},
		Faults:               []media.FaultKind{media.FaultNone, media.FaultTransformTimeout},
		Repetitions:          1,
		TransformConcurrency: 1,
		RequestTimeout:       2 * time.Second,
		TransformTimeout:     time.Second,
		SlotWaitLimit:        200 * time.Millisecond,
		FaultDelay:           500 * time.Millisecond,
		KillSwitchOnDelay:    500 * time.Millisecond,
		KillSwitchOffDelay:   500 * time.Millisecond,
		NormalDuration:       2 * time.Second,
		FaultDuration:        3 * time.Second,
		RecoveryDuration:     3 * time.Second,
		HitRate:              20,
		HealthyMissRate:      2,
		PoisonedMissRate:     4,
		HitKeys:              2,
		MaxInflight:          200,
		ResourceSampleGap:    100 * time.Millisecond,
		StateSampleGap:       200 * time.Millisecond,
		TimelineBucket:       time.Second,
		transformer:          &media.DeterministicTransformer{Delay: 20 * time.Millisecond, Output: []byte("webp-bytes")},
	}
}

func TestExecuteAndAnalyzeEndToEnd(t *testing.T) {
	config := testConfig(t)
	output, err := Execute(config)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(output.Trials) != 6 {
		t.Fatalf("trials = %d, want 6", len(output.Trials))
	}
	byID := make(map[string]TrialResult)
	for _, trial := range output.Trials {
		if !trial.Valid {
			t.Errorf("trial %s invalid: %s", trial.TrialID, trial.InvalidReason)
		}
		byID[trial.TrialID] = trial
	}
	if t.Failed() {
		t.FailNow()
	}

	baseline := byID["baseline-transform-timeout-r01"]
	if miss := baseline.Summaries[StreamHealthyMiss][PhaseFault]; miss.Errors == 0 || miss.HTTP500 == 0 {
		t.Fatalf("baseline healthy miss during fault = %+v, want timeouts surfacing as 500", miss)
	}
	if baseline.ShedRequests != 0 || baseline.KillSwitchRequests != 0 {
		t.Fatalf("baseline isolation outcomes = shed %d kill %d, want none", baseline.ShedRequests, baseline.KillSwitchRequests)
	}
	bounded := byID["bounded-wait-transform-timeout-r01"]
	if bounded.ShedRequests == 0 || bounded.TransformShed != int64(bounded.ShedRequests) {
		t.Fatalf("bounded-wait shed = rows %d metric %d, want matching positive counts", bounded.ShedRequests, bounded.TransformShed)
	}
	killSwitch := byID["kill-switch-transform-timeout-r01"]
	if killSwitch.KillSwitchRequests == 0 || killSwitch.KillSwitchOnOffsetMS < 0 || killSwitch.KillSwitchOffOffsetMS < 0 {
		t.Fatalf("kill-switch trial = %+v, want rejections and both transitions", killSwitch)
	}
	for _, id := range []string{"baseline-none-r01", "bounded-wait-none-r01", "kill-switch-none-r01"} {
		trial := byID[id]
		if trial.ErrorRequests != 0 || trial.FaultInjections != 0 {
			t.Fatalf("%s errors = %d injections = %d, want 0/0", id, trial.ErrorRequests, trial.FaultInjections)
		}
		if hit := trial.Summaries[StreamHit]["all"]; hit.Requests < 100 || hit.Success != hit.Requests {
			t.Fatalf("%s hit stream = %+v", id, hit)
		}
	}
	for _, trial := range output.Trials {
		if trial.FaultInjections == 0 && trial.Fault != string(media.FaultNone) {
			t.Fatalf("%s recorded no fault injections", trial.TrialID)
		}
	}

	for _, name := range []string{"run.json", "trials.csv", "requests.csv.gz", "state.csv", "resources.csv.gz", "metrics.prom", "logs.jsonl"} {
		if info, err := os.Stat(filepath.Join(output.Directory, name)); err != nil || info.Size() == 0 {
			t.Fatalf("raw file %s missing or empty: %v", name, err)
		}
	}

	analysis, err := Analyze(output.Directory)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if analysis.ValidTrials != 6 || analysis.InvalidTrials != 0 {
		t.Fatalf("analysis valid/invalid = %d/%d", analysis.ValidTrials, analysis.InvalidTrials)
	}
	if analysis.Thresholds[StreamHit] <= 0 || analysis.Thresholds[StreamHealthyMiss] <= 0 {
		t.Fatalf("thresholds = %v", analysis.Thresholds)
	}
	var sawBoundedShed, sawBaselineImpact bool
	for _, row := range analysis.BlastRadius {
		if row.Mode == "bounded-wait" && row.Fault == "transform-timeout" && row.Stream == StreamHealthyMiss && row.Phase == PhaseFault && row.ShedRateMean > 0 {
			sawBoundedShed = true
		}
		if row.Mode == "baseline" && row.Fault == "transform-timeout" && row.Stream == StreamHealthyMiss && row.Phase == PhaseFault && row.AffectedRateMean > 0 {
			sawBaselineImpact = true
		}
	}
	if !sawBoundedShed || !sawBaselineImpact {
		t.Fatalf("blast radius rows missing expected signals: shed=%t impact=%t", sawBoundedShed, sawBaselineImpact)
	}
	if len(analysis.Intervals) != 3 || len(analysis.NormalCost) != 6 {
		t.Fatalf("intervals = %d normal cost = %d", len(analysis.Intervals), len(analysis.NormalCost))
	}
	for _, name := range []string{"analysis.json", "blast-radius.csv", "resources.csv", "normal-cost.csv", "intervals.csv", "timeline-summary.csv", "timeline-none.svg", "timeline-transform-timeout.svg"} {
		if info, err := os.Stat(filepath.Join(analysis.Directory, name)); err != nil || info.Size() == 0 {
			t.Fatalf("analysis file %s missing or empty: %v", name, err)
		}
	}

	first, err := os.ReadFile(filepath.Join(analysis.Directory, "analysis.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Analyze(output.Directory); err != nil {
		t.Fatalf("second Analyze: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(analysis.Directory, "analysis.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("analysis.json is not reproducible from the same raw data")
	}

	if _, err := Execute(config); err == nil {
		t.Fatal("Execute overwrote an existing result directory")
	}
}

func TestSpecsAndScheduleAreUniqueAndOrdered(t *testing.T) {
	t.Parallel()

	seen := make(map[string]bool)
	for n := range 20000 {
		spec := MissSpec(n)
		if spec.Width < 600 || spec.Width > 680 || spec.Height < 600 || spec.Height > 680 || spec.Quality < 70 || spec.Quality > 90 {
			t.Fatalf("spec %d out of range: %+v", n, spec)
		}
		if seen[spec.Canonical()] {
			t.Fatalf("duplicate miss spec at %d: %s", n, spec.Canonical())
		}
		seen[spec.Canonical()] = true
	}
	hits, err := HitSpecs(4)
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range hits {
		if seen[hit.Canonical()] {
			t.Fatalf("hit spec collides with miss specs: %s", hit.Canonical())
		}
	}
	source := "de206136ed0eeaa42499840f49fa35bd3d57745b8a9580b16f0d6e6393ac7f91"
	poisoned := PoisonedSourceHash(source)
	if poisoned == source || media.ValidateSourceHash(poisoned) != nil {
		t.Fatalf("poisoned hash = %s", poisoned)
	}
	scopedA, scopedB := RunScopedSourceHash("run-a", source), RunScopedSourceHash("run-b", source)
	if scopedA == scopedB || scopedA == source || media.ValidateSourceHash(scopedA) != nil || PoisonedSourceHash(scopedA) == PoisonedSourceHash(scopedB) {
		t.Fatalf("run-scoped hashes are not distinct: %s %s", scopedA, scopedB)
	}

	config := Config{
		Modes:       []media.IsolationMode{media.IsolationBaseline, media.IsolationBoundedWait},
		Faults:      []media.FaultKind{media.FaultNone, media.FaultSlowTransform},
		Repetitions: 2,
	}
	schedule := buildSchedule(config)
	if len(schedule) != 8 {
		t.Fatalf("schedule length = %d, want 8", len(schedule))
	}
	ids := make(map[string]bool)
	for _, item := range schedule {
		if ids[item.ID] {
			t.Fatalf("duplicate trial ID %s", item.ID)
		}
		ids[item.ID] = true
	}
	if schedule[0].Mode == schedule[2].Mode {
		t.Fatalf("mode order does not rotate between faults: %+v", schedule[:4])
	}
}

func TestValidateConfigRejectsUnsafeTimeouts(t *testing.T) {
	t.Parallel()

	config := testConfig(t)
	config.RequestTimeout = config.TransformTimeout
	if err := validateConfig(config); err == nil {
		t.Fatal("request timeout equal to transform timeout accepted")
	}
	config = testConfig(t)
	config.FaultDelay = config.TransformTimeout
	if err := validateConfig(config); err == nil {
		t.Fatal("fault delay equal to transform timeout accepted")
	}
	config = testConfig(t)
	config.KillSwitchOnDelay = config.FaultDuration
	if err := validateConfig(config); err == nil {
		t.Fatal("kill switch delay outside the fault phase accepted")
	}
	config = testConfig(t)
	config.Storage = StorageS3
	if err := validateConfig(config); err == nil {
		t.Fatal("S3 storage without buckets accepted")
	}
}
