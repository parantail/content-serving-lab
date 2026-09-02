package media

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const (
	CoordinatorNone          = "none"
	CoordinatorProcessSingle = "process-singleflight"
)

type NoneCoordinator struct {
	workTimeout time.Duration
}

func NewNoneCoordinator(workTimeout time.Duration) *NoneCoordinator {
	return &NoneCoordinator{workTimeout: workTimeout}
}

func (c *NoneCoordinator) Do(requestCtx context.Context, _ string, work Work) ([]byte, bool, error) {
	workCtx, cancel := context.WithTimeout(requestCtx, c.workTimeout)
	defer cancel()
	data, err := work(workCtx)
	return data, false, err
}

func (*NoneCoordinator) Mode() string {
	return CoordinatorNone
}

type flightCall struct {
	done    chan struct{}
	data    []byte
	err     error
	waiters int
}

type ProcessCoordinator struct {
	workTimeout time.Duration

	mu       sync.Mutex
	inflight map[string]*flightCall
}

// ProcessCoordinatorSnapshot exposes coordination state for deterministic
// failure experiments without changing request execution semantics.
type ProcessCoordinatorSnapshot struct {
	Keys    int
	Waiters int
}

func NewProcessCoordinator(workTimeout time.Duration) *ProcessCoordinator {
	return &ProcessCoordinator{
		workTimeout: workTimeout,
		inflight:    make(map[string]*flightCall),
	}
}

func (c *ProcessCoordinator) Do(requestCtx context.Context, derivativeKey string, work Work) ([]byte, bool, error) {
	c.mu.Lock()
	if call, ok := c.inflight[derivativeKey]; ok {
		call.waiters++
		c.mu.Unlock()
		return waitForCall(requestCtx, call, true)
	}

	call := &flightCall{done: make(chan struct{})}
	c.inflight[derivativeKey] = call
	c.mu.Unlock()

	go c.run(derivativeKey, call, work)
	return waitForCall(requestCtx, call, false)
}

func (c *ProcessCoordinator) run(derivativeKey string, call *flightCall, work Work) {
	workCtx, cancel := context.WithTimeout(context.Background(), c.workTimeout)
	defer cancel()

	call.data, call.err = work(workCtx)

	c.mu.Lock()
	delete(c.inflight, derivativeKey)
	close(call.done)
	c.mu.Unlock()
}

func (c *ProcessCoordinator) Snapshot() ProcessCoordinatorSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	snapshot := ProcessCoordinatorSnapshot{Keys: len(c.inflight)}
	for _, call := range c.inflight {
		snapshot.Waiters += call.waiters
	}
	return snapshot
}

func waitForCall(requestCtx context.Context, call *flightCall, coalesced bool) ([]byte, bool, error) {
	select {
	case <-requestCtx.Done():
		return nil, coalesced, requestCtx.Err()
	case <-call.done:
		return call.data, coalesced, call.err
	}
}

func (*ProcessCoordinator) Mode() string {
	return CoordinatorProcessSingle
}

func NewCoordinator(mode string, workTimeout time.Duration) (Coordinator, error) {
	switch mode {
	case CoordinatorNone:
		return NewNoneCoordinator(workTimeout), nil
	case CoordinatorProcessSingle:
		return NewProcessCoordinator(workTimeout), nil
	default:
		return nil, fmt.Errorf("unknown coordinator mode %q", mode)
	}
}
