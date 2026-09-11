package media

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// FaultKind names a deterministic fault that the experiment can apply to
// requests for one poisoned source hash. Requests for any other source are
// never touched.
type FaultKind string

const (
	FaultNone             FaultKind = "none"
	FaultSlowTransform    FaultKind = "slow-transform"
	FaultTransformTimeout FaultKind = "transform-timeout"
	FaultTransformError   FaultKind = "transform-error"
	FaultSlowOriginal     FaultKind = "slow-original"
)

var faultKinds = []FaultKind{FaultNone, FaultSlowTransform, FaultTransformTimeout, FaultTransformError, FaultSlowOriginal}

// ErrInjectedTransformFailure is the error returned by the transform-error
// fault. It is distinct from real transformer errors so raw data can tell
// them apart.
var ErrInjectedTransformFailure = errors.New("injected transform failure")

func ParseFaultKind(raw string) (FaultKind, error) {
	kind := FaultKind(strings.ToLower(strings.TrimSpace(raw)))
	for _, known := range faultKinds {
		if kind == known {
			return kind, nil
		}
	}
	return "", fmt.Errorf("unknown fault kind %q", raw)
}

type FaultState struct {
	Kind  FaultKind
	Delay time.Duration
	Since time.Time
}

// FaultInjector applies the active fault to the poisoned source hash. The
// processor calls its hooks before reading the original and before running
// the transform; a hook that returns an error replaces the real step.
type FaultInjector struct {
	sourceHash string
	metrics    *Metrics
	now        func() time.Time

	mu    sync.RWMutex
	state FaultState
}

func NewFaultInjector(poisonedSourceHash string, metrics *Metrics) (*FaultInjector, error) {
	if err := ValidateSourceHash(poisonedSourceHash); err != nil {
		return nil, fmt.Errorf("poisoned source hash: %w", err)
	}
	if metrics == nil {
		metrics = &Metrics{}
	}
	return &FaultInjector{
		sourceHash: strings.ToLower(poisonedSourceHash),
		metrics:    metrics,
		now:        time.Now,
		state:      FaultState{Kind: FaultNone},
	}, nil
}

func (f *FaultInjector) SourceHash() string {
	return f.sourceHash
}

func (f *FaultInjector) State() FaultState {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.state
}

// Set replaces the active fault. slow-transform and slow-original need a
// positive delay, transform-error accepts an optional delay before failing,
// and transform-timeout ignores the delay because it blocks until the work
// context ends.
func (f *FaultInjector) Set(kind FaultKind, delay time.Duration) (FaultState, error) {
	if _, err := ParseFaultKind(string(kind)); err != nil {
		return FaultState{}, err
	}
	if delay < 0 {
		return FaultState{}, errors.New("fault delay must not be negative")
	}
	switch kind {
	case FaultSlowTransform, FaultSlowOriginal:
		if delay <= 0 {
			return FaultState{}, fmt.Errorf("fault %s requires a positive delay", kind)
		}
	case FaultNone, FaultTransformTimeout:
		delay = 0
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = FaultState{Kind: kind, Delay: delay, Since: f.now()}
	return f.state, nil
}

func (f *FaultInjector) matches(sourceHash string) (FaultState, bool) {
	if !strings.EqualFold(sourceHash, f.sourceHash) {
		return FaultState{}, false
	}
	state := f.State()
	if state.Kind == FaultNone {
		return FaultState{}, false
	}
	return state, true
}

func (f *FaultInjector) beforeOriginalRead(ctx context.Context, sourceHash string) error {
	state, ok := f.matches(sourceHash)
	if !ok || state.Kind != FaultSlowOriginal {
		return nil
	}
	f.metrics.faultSlowOriginal.Add(1)
	return sleepWithContext(ctx, state.Delay)
}

func (f *FaultInjector) beforeTransform(ctx context.Context, sourceHash string) error {
	state, ok := f.matches(sourceHash)
	if !ok {
		return nil
	}
	switch state.Kind {
	case FaultSlowTransform:
		f.metrics.faultSlowTransform.Add(1)
		return sleepWithContext(ctx, state.Delay)
	case FaultTransformTimeout:
		f.metrics.faultTransformTimeout.Add(1)
		<-ctx.Done()
		return ctx.Err()
	case FaultTransformError:
		f.metrics.faultTransformError.Add(1)
		if err := sleepWithContext(ctx, state.Delay); err != nil {
			return err
		}
		return ErrInjectedTransformFailure
	default:
		return nil
	}
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
