package media

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

const poisonedTestSourceHash = "abababababababababababababababababababababababababababababababab"

// blockingTransformer holds every transform until release is closed so tests
// can fill transform slots deterministically.
type blockingTransformer struct {
	calls   atomic.Int64
	started chan struct{}
	release chan struct{}
}

func newBlockingTransformer() *blockingTransformer {
	return &blockingTransformer{started: make(chan struct{}, 64), release: make(chan struct{})}
}

func (t *blockingTransformer) Transform(ctx context.Context, _ []byte, _ TransformSpec) ([]byte, error) {
	t.calls.Add(1)
	t.started <- struct{}{}
	select {
	case <-t.release:
		return []byte("blocking-result"), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (*blockingTransformer) Version() string { return "blocking-v1" }

func newIsolationProcessor(
	t *testing.T,
	mode IsolationMode,
	concurrency int,
	waitLimit time.Duration,
	coordinatorMode string,
	workTimeout time.Duration,
	transformer Transformer,
	withFaults bool,
) *Processor {
	t.Helper()
	directory := t.TempDir()
	originalPath := filepath.Join(directory, "original.jpg")
	if err := os.WriteFile(originalPath, []byte("original-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	derivatives, err := NewLocalDerivativeStore(filepath.Join(directory, "derivatives"))
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(coordinatorMode, workTimeout)
	if err != nil {
		t.Fatal(err)
	}
	metrics := &Metrics{}
	isolation, err := NewIsolation(mode, concurrency, waitLimit, metrics)
	if err != nil {
		t.Fatal(err)
	}
	if withFaults {
		isolation.Faults, err = NewFaultInjector(poisonedTestSourceHash, metrics)
		if err != nil {
			t.Fatal(err)
		}
	}
	processor, err := NewProcessorWithIsolation(
		NewFileOriginalStore(map[string]string{testSourceHash: originalPath, poisonedTestSourceHash: originalPath}),
		derivatives,
		transformer,
		coordinator,
		metrics,
		isolation,
	)
	if err != nil {
		t.Fatal(err)
	}
	return processor
}

func specWithWidth(t *testing.T, width int) TransformSpec {
	t.Helper()
	spec, err := ParseTransformSpec("width="+strconv.Itoa(width)+",height=640,fit=cover,quality=80", "webp")
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestTransformGateShedsAfterWaitLimit(t *testing.T) {
	t.Parallel()

	gate, err := NewTransformGate(1, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	release, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gate.Inflight() != 1 {
		t.Fatalf("inflight = %d, want 1", gate.Inflight())
	}

	started := time.Now()
	if _, err := gate.Acquire(context.Background()); !errors.Is(err, ErrTransformShed) {
		t.Fatalf("second acquire error = %v, want ErrTransformShed", err)
	}
	if elapsed := time.Since(started); elapsed < 50*time.Millisecond {
		t.Fatalf("shed after %v, want at least the 50ms wait limit", elapsed)
	}
	if gate.Waiting() != 0 {
		t.Fatalf("waiting = %d after shed, want 0", gate.Waiting())
	}

	release()
	release, err = gate.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	release()
}

func TestTransformGateWithoutLimitWaitsForContext(t *testing.T) {
	t.Parallel()

	gate, err := NewTransformGate(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	release, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := gate.Acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquire error = %v, want context deadline", err)
	}

	for _, tt := range []struct {
		concurrency int
		waitLimit   time.Duration
	}{{0, 0}, {1, -time.Second}} {
		if _, err := NewTransformGate(tt.concurrency, tt.waitLimit); err == nil {
			t.Fatalf("NewTransformGate(%d, %v) accepted invalid arguments", tt.concurrency, tt.waitLimit)
		}
	}
}

func TestBoundedWaitShedsWithoutReadingOriginal(t *testing.T) {
	t.Parallel()

	transformer := newBlockingTransformer()
	processor := newIsolationProcessor(t, IsolationBoundedWait, 1, 50*time.Millisecond, CoordinatorProcessSingle, 5*time.Second, transformer, false)

	leaderDone := make(chan error, 1)
	go func() {
		_, err := processor.GetDerivative(context.Background(), testSourceHash, specWithWidth(t, 1))
		leaderDone <- err
	}()
	<-transformer.started

	started := time.Now()
	result, err := processor.GetDerivative(context.Background(), testSourceHash, specWithWidth(t, 2))
	if !errors.Is(err, ErrTransformShed) {
		t.Fatalf("healthy miss error = %v, want ErrTransformShed", err)
	}
	if elapsed := time.Since(started); elapsed < 50*time.Millisecond {
		t.Fatalf("shed after %v, want at least the wait limit", elapsed)
	}
	if result.Isolation != IsolationOutcomeShed || result.Cache != "miss" {
		t.Fatalf("result = %+v, want shed miss", result)
	}

	snapshot := processor.Metrics().Snapshot()
	if snapshot.TransformShed != 1 {
		t.Fatalf("shed = %d, want 1", snapshot.TransformShed)
	}
	if snapshot.OriginalSuccess != 1 {
		t.Fatalf("original reads = %d, want 1 (shed request must not read the original)", snapshot.OriginalSuccess)
	}
	if snapshot.TransformWaitCount != 2 {
		t.Fatalf("wait count = %d, want 2", snapshot.TransformWaitCount)
	}
	if transformer.calls.Load() != 1 {
		t.Fatalf("transformer calls = %d, want 1", transformer.calls.Load())
	}

	close(transformer.release)
	if err := <-leaderDone; err != nil {
		t.Fatalf("leader error = %v", err)
	}
	if got := processor.Metrics().Snapshot().OriginalBytesInflight; got != 0 {
		t.Fatalf("original bytes inflight = %d after completion, want 0", got)
	}
}

func TestBaselineReadsOriginalBeforeWaitingForSlot(t *testing.T) {
	t.Parallel()

	transformer := newBlockingTransformer()
	processor := newIsolationProcessor(t, IsolationBaseline, 1, 0, CoordinatorNone, 5*time.Second, transformer, false)

	leaderDone := make(chan error, 1)
	go func() {
		_, err := processor.GetDerivative(context.Background(), testSourceHash, specWithWidth(t, 1))
		leaderDone <- err
	}()
	<-transformer.started

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	result, err := processor.GetDerivative(ctx, testSourceHash, specWithWidth(t, 2))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("healthy miss error = %v, want context deadline", err)
	}
	if result.Isolation != "" {
		t.Fatalf("isolation outcome = %q, want empty in baseline", result.Isolation)
	}

	snapshot := processor.Metrics().Snapshot()
	if snapshot.OriginalSuccess != 2 {
		t.Fatalf("original reads = %d, want 2 (baseline reads before waiting)", snapshot.OriginalSuccess)
	}
	if snapshot.TransformShed != 0 {
		t.Fatalf("shed = %d, want 0 in baseline", snapshot.TransformShed)
	}
	if processor.State().TransformWaiting != 0 {
		t.Fatalf("waiting = %d after timeout, want 0", processor.State().TransformWaiting)
	}

	close(transformer.release)
	if err := <-leaderDone; err != nil {
		t.Fatalf("leader error = %v", err)
	}
}

func TestKillSwitchRejectsMissesAndServesHits(t *testing.T) {
	t.Parallel()

	transformer := &DeterministicTransformer{Delay: time.Millisecond, Output: []byte("webp-result")}
	processor := newIsolationProcessor(t, IsolationKillSwitch, 4, 0, CoordinatorProcessSingle, time.Second, transformer, false)
	killSwitch := processor.Isolation().KillSwitch

	if _, err := processor.GetDerivative(context.Background(), testSourceHash, specWithWidth(t, 1)); err != nil {
		t.Fatal(err)
	}

	if changed, _ := killSwitch.Set(true); !changed {
		t.Fatal("enabling the kill switch reported no change")
	}
	if changed, _ := killSwitch.Set(true); changed {
		t.Fatal("re-enabling the kill switch reported a change")
	}

	hit, err := processor.GetDerivative(context.Background(), testSourceHash, specWithWidth(t, 1))
	if err != nil || hit.Cache != "derivative" {
		t.Fatalf("hit result = %+v, err = %v, want derivative hit", hit, err)
	}

	miss, err := processor.GetDerivative(context.Background(), testSourceHash, specWithWidth(t, 2))
	if !errors.Is(err, ErrTransformDisabled) {
		t.Fatalf("miss error = %v, want ErrTransformDisabled", err)
	}
	if miss.Isolation != IsolationOutcomeKillSwitch || miss.Cache != "miss" {
		t.Fatalf("miss result = %+v, want kill-switch miss", miss)
	}

	snapshot := processor.Metrics().Snapshot()
	if snapshot.KillSwitchRejected != 1 || snapshot.KillSwitchState != 1 {
		t.Fatalf("kill switch metrics = rejected %d state %d, want 1/1", snapshot.KillSwitchRejected, snapshot.KillSwitchState)
	}
	if transformer.Calls() != 1 {
		t.Fatalf("transformer calls = %d, want 1", transformer.Calls())
	}

	killSwitch.Set(false)
	if _, err := processor.GetDerivative(context.Background(), testSourceHash, specWithWidth(t, 2)); err != nil {
		t.Fatalf("miss after disabling: %v", err)
	}
	if got := len(killSwitch.Transitions()); got != 2 {
		t.Fatalf("transitions = %d, want 2", got)
	}
	if processor.Metrics().Snapshot().KillSwitchState != 0 {
		t.Fatal("kill switch state metric did not return to 0")
	}
}

func TestIsolationValidation(t *testing.T) {
	t.Parallel()

	if _, err := ParseIsolationMode("multi-region"); err == nil {
		t.Fatal("unknown isolation mode accepted")
	}
	if _, err := NewIsolation(IsolationBoundedWait, 4, 0, &Metrics{}); err == nil {
		t.Fatal("bounded-wait without wait limit accepted")
	}

	gate, err := NewTransformGate(4, 0)
	if err != nil {
		t.Fatal(err)
	}
	boundedGate, err := NewTransformGate(4, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	invalid := []Isolation{
		{Mode: IsolationBaseline},
		{Mode: IsolationBaseline, Gate: gate, KillSwitch: NewKillSwitch(nil)},
		{Mode: IsolationBaseline, Gate: boundedGate},
		{Mode: IsolationBoundedWait, Gate: gate},
		{Mode: IsolationBoundedWait, Gate: boundedGate, KillSwitch: NewKillSwitch(nil)},
		{Mode: IsolationKillSwitch, Gate: gate},
		{Mode: IsolationKillSwitch, Gate: boundedGate, KillSwitch: NewKillSwitch(nil)},
	}
	for index, isolation := range invalid {
		if _, err := NewProcessorWithIsolation(nil, nil, nil, nil, &Metrics{}, isolation); err == nil {
			t.Fatalf("invalid isolation %d accepted: %+v", index, isolation)
		}
	}

	legacy := NewProcessor(nil, nil, nil, NewNoneCoordinator(time.Second), &Metrics{})
	if legacy.Isolation() != nil {
		t.Fatal("legacy processor reports isolation controls")
	}
}

func TestFaultInjectorTargetsOnlyPoisonedSource(t *testing.T) {
	t.Parallel()

	transformer := &DeterministicTransformer{Delay: time.Millisecond, Output: []byte("webp-result")}
	processor := newIsolationProcessor(t, IsolationBaseline, 4, 0, CoordinatorNone, 200*time.Millisecond, transformer, true)
	faults := processor.Isolation().Faults
	width := 0
	next := func() TransformSpec {
		width++
		return specWithWidth(t, width)
	}
	run := func(sourceHash string) (time.Duration, error) {
		started := time.Now()
		_, err := processor.GetDerivative(context.Background(), sourceHash, next())
		return time.Since(started), err
	}

	if _, err := run(poisonedTestSourceHash); err != nil {
		t.Fatalf("poisoned request without fault: %v", err)
	}

	if _, err := faults.Set(FaultSlowTransform, 80*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if elapsed, err := run(poisonedTestSourceHash); err != nil || elapsed < 80*time.Millisecond {
		t.Fatalf("slow-transform poisoned: elapsed %v err %v, want >= 80ms success", elapsed, err)
	}
	if _, err := run(testSourceHash); err != nil {
		t.Fatalf("healthy request during slow-transform: %v", err)
	}
	if got := processor.Metrics().Snapshot().FaultSlowTransform; got != 1 {
		t.Fatalf("slow-transform injections = %d, want 1 (healthy request must not count)", got)
	}

	if _, err := faults.Set(FaultTransformError, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := run(poisonedTestSourceHash); !errors.Is(err, ErrInjectedTransformFailure) {
		t.Fatalf("transform-error poisoned error = %v, want ErrInjectedTransformFailure", err)
	}
	if _, err := run(testSourceHash); err != nil {
		t.Fatalf("healthy request during transform-error: %v", err)
	}
	if snapshot := processor.Metrics().Snapshot(); snapshot.TransformError != 1 || snapshot.FaultTransformError != 1 {
		t.Fatalf("transform-error metrics = %+v", snapshot)
	}

	if _, err := faults.Set(FaultTransformTimeout, 0); err != nil {
		t.Fatal(err)
	}
	if elapsed, err := run(poisonedTestSourceHash); !errors.Is(err, context.DeadlineExceeded) || elapsed < 200*time.Millisecond {
		t.Fatalf("transform-timeout poisoned: elapsed %v err %v, want deadline after >= 200ms", elapsed, err)
	}
	if snapshot := processor.Metrics().Snapshot(); snapshot.TransformTimeout != 1 || snapshot.FaultTransformTimeout != 1 {
		t.Fatalf("transform-timeout metrics = %+v", snapshot)
	}

	if _, err := faults.Set(FaultSlowOriginal, 80*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if elapsed, err := run(poisonedTestSourceHash); err != nil || elapsed < 80*time.Millisecond {
		t.Fatalf("slow-original poisoned: elapsed %v err %v, want >= 80ms success", elapsed, err)
	}
	if _, err := run(testSourceHash); err != nil {
		t.Fatalf("healthy request during slow-original: %v", err)
	}
	if got := processor.Metrics().Snapshot().FaultSlowOriginal; got != 1 {
		t.Fatalf("slow-original injections = %d, want 1", got)
	}

	if _, err := faults.Set(FaultNone, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := run(poisonedTestSourceHash); err != nil {
		t.Fatalf("poisoned request after clearing fault: %v", err)
	}
	if faults.State().Kind != FaultNone {
		t.Fatalf("fault state = %+v, want none", faults.State())
	}
	if got := processor.Metrics().Snapshot().OriginalBytesInflight; got != 0 {
		t.Fatalf("original bytes inflight = %d, want 0", got)
	}

	for _, tt := range []struct {
		kind  FaultKind
		delay time.Duration
	}{
		{FaultSlowTransform, 0},
		{FaultSlowOriginal, 0},
		{FaultTransformError, -time.Second},
		{FaultKind("network-partition"), time.Second},
	} {
		if _, err := faults.Set(tt.kind, tt.delay); err == nil {
			t.Fatalf("Set(%q, %v) accepted invalid fault", tt.kind, tt.delay)
		}
	}
	if _, err := NewFaultInjector("not-a-hash", nil); err == nil {
		t.Fatal("invalid poisoned hash accepted")
	}
}
