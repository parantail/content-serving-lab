package media

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

type DeterministicTransformer struct {
	Delay     time.Duration
	Output    []byte
	Err       error
	FailFirst int64

	calls       atomic.Int64
	inflight    atomic.Int64
	maxInflight atomic.Int64
	mu          sync.Mutex
	callErrors  []error
}

func (t *DeterministicTransformer) Transform(ctx context.Context, _ []byte, _ TransformSpec) ([]byte, error) {
	call := t.calls.Add(1)
	inflight := t.inflight.Add(1)
	defer t.inflight.Add(-1)
	for {
		current := t.maxInflight.Load()
		if inflight <= current || t.maxInflight.CompareAndSwap(current, inflight) {
			break
		}
	}

	timer := time.NewTimer(t.Delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		t.recordError(ctx.Err())
		return nil, ctx.Err()
	case <-timer.C:
	}

	if call <= t.FailFirst || t.Err != nil {
		err := t.Err
		if err == nil {
			err = errors.New("deterministic transform failure")
		}
		t.recordError(err)
		return nil, err
	}
	return append([]byte(nil), t.Output...), nil
}

func (*DeterministicTransformer) Version() string {
	return "deterministic-v1"
}

func (t *DeterministicTransformer) Calls() int64 {
	return t.calls.Load()
}

func (t *DeterministicTransformer) MaxInflight() int64 {
	return t.maxInflight.Load()
}

func (t *DeterministicTransformer) recordError(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.callErrors = append(t.callErrors, err)
}
