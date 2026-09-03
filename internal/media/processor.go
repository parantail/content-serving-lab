package media

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Processor struct {
	originals   OriginalStore
	derivatives DerivativeStore
	transformer Transformer
	coordinator Coordinator
	metrics     *Metrics
}

type DerivativeResult struct {
	Data      []byte
	Key       string
	Cache     string
	Coalesced bool
}

type ProcessorState struct {
	TransformInflight  int64
	CoordinatorKeys    int
	CoordinatorWaiters int
}

func NewProcessor(
	originals OriginalStore,
	derivatives DerivativeStore,
	transformer Transformer,
	coordinator Coordinator,
	metrics *Metrics,
) *Processor {
	return &Processor{
		originals:   originals,
		derivatives: derivatives,
		transformer: transformer,
		coordinator: coordinator,
		metrics:     metrics,
	}
}

func (p *Processor) GetDerivative(ctx context.Context, sourceHash string, spec TransformSpec) (result DerivativeResult, resultErr error) {
	started := time.Now()
	requestCache := "miss"
	key, err := DerivativeKey(sourceHash, spec)
	if err != nil {
		return DerivativeResult{}, err
	}
	defer func() {
		p.metrics.requestFinished(requestCache, resultErr == nil, time.Since(started).Nanoseconds())
	}()

	data, found, err := p.derivatives.Get(ctx, key)
	if err != nil {
		return DerivativeResult{}, fmt.Errorf("read derivative: %w", err)
	}
	if found {
		p.metrics.derivativeHits.Add(1)
		requestCache = "derivative"
		return DerivativeResult{Data: data, Key: key, Cache: "derivative"}, nil
	}
	p.metrics.derivativeMisses.Add(1)

	data, coalesced, err := p.coordinator.Do(ctx, key, func(workCtx context.Context) ([]byte, error) {
		return p.createDerivative(workCtx, sourceHash, key, spec)
	})
	if coalesced {
		p.metrics.coalesced.Add(1)
	}
	if err != nil {
		return DerivativeResult{Key: key, Cache: "miss", Coalesced: coalesced}, err
	}
	return DerivativeResult{Data: data, Key: key, Cache: "miss", Coalesced: coalesced}, nil
}

func (p *Processor) createDerivative(ctx context.Context, sourceHash, key string, spec TransformSpec) ([]byte, error) {
	if data, found, err := p.derivatives.Get(ctx, key); err != nil {
		return nil, fmt.Errorf("recheck derivative: %w", err)
	} else if found {
		return data, nil
	}

	original, err := p.originals.Read(ctx, sourceHash)
	if err != nil {
		p.metrics.originalError.Add(1)
		return nil, fmt.Errorf("read original: %w", err)
	}
	p.metrics.originalSuccess.Add(1)

	p.metrics.transformStarted()
	started := time.Now()
	data, err := p.transformer.Transform(ctx, original, spec)
	p.metrics.transformDurationNanos.Add(time.Since(started).Nanoseconds())
	p.metrics.transformInflight.Add(-1)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			p.metrics.transformTimeout.Add(1)
		} else {
			p.metrics.transformError.Add(1)
		}
		return nil, fmt.Errorf("transform original: %w", err)
	}
	p.metrics.transformSuccess.Add(1)

	created, err := p.derivatives.PutIfAbsent(ctx, key, data)
	if err != nil {
		p.metrics.publishError.Add(1)
		return nil, fmt.Errorf("publish derivative: %w", err)
	}
	if created {
		p.metrics.publishCreated.Add(1)
		return data, nil
	}
	p.metrics.publishExisting.Add(1)

	winner, found, err := p.derivatives.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("read published derivative: %w", err)
	}
	if !found {
		return nil, errors.New("published derivative is missing")
	}
	return winner, nil
}

func (p *Processor) Metrics() *Metrics {
	return p.metrics
}

func (p *Processor) CoordinatorMode() string {
	return p.coordinator.Mode()
}

func (p *Processor) State() ProcessorState {
	state := ProcessorState{TransformInflight: p.metrics.Snapshot().TransformInflight}
	if coordinator, ok := p.coordinator.(interface {
		Snapshot() ProcessCoordinatorSnapshot
	}); ok {
		snapshot := coordinator.Snapshot()
		state.CoordinatorKeys = snapshot.Keys
		state.CoordinatorWaiters = snapshot.Waiters
	}
	return state
}
