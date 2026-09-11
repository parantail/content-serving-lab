package media

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// ErrTransformShed is returned when a request waited longer than the gate's
// wait limit for a transform slot. Callers translate it into a fast failure
// instead of holding the request until the transform timeout.
var ErrTransformShed = errors.New("transform slot wait limit exceeded")

// TransformGate limits the number of transforms that run at the same time in
// one process. With a zero wait limit a request waits until a slot frees or
// its context ends. With a positive wait limit the request is shed once the
// limit passes.
type TransformGate struct {
	slots     chan struct{}
	waitLimit time.Duration
	waiting   atomic.Int64
}

func NewTransformGate(concurrency int, waitLimit time.Duration) (*TransformGate, error) {
	if concurrency < 1 {
		return nil, errors.New("media: transform gate concurrency must be positive")
	}
	if waitLimit < 0 {
		return nil, errors.New("media: transform gate wait limit must not be negative")
	}
	return &TransformGate{slots: make(chan struct{}, concurrency), waitLimit: waitLimit}, nil
}

func (g *TransformGate) Concurrency() int {
	return cap(g.slots)
}

func (g *TransformGate) WaitLimit() time.Duration {
	return g.waitLimit
}

// Inflight reports how many slots are held right now.
func (g *TransformGate) Inflight() int {
	return len(g.slots)
}

// Waiting reports how many requests are blocked waiting for a slot.
func (g *TransformGate) Waiting() int64 {
	return g.waiting.Load()
}

// Acquire returns a release function once a slot is held. The caller must
// call release exactly once.
func (g *TransformGate) Acquire(ctx context.Context) (release func(), err error) {
	release = func() { <-g.slots }
	select {
	case g.slots <- struct{}{}:
		return release, nil
	default:
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	g.waiting.Add(1)
	defer g.waiting.Add(-1)

	var limit <-chan time.Time
	if g.waitLimit > 0 {
		timer := time.NewTimer(g.waitLimit)
		defer timer.Stop()
		limit = timer.C
	}
	select {
	case g.slots <- struct{}{}:
		return release, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-limit:
		return nil, ErrTransformShed
	}
}
