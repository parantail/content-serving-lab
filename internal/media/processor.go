package media

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	IsolationOutcomeShed       = "shed"
	IsolationOutcomeKillSwitch = "kill-switch"
)

type Processor struct {
	originals   OriginalStore
	derivatives DerivativeStore
	transformer Transformer
	coordinator Coordinator
	metrics     *Metrics
	isolation   *Isolation
}

type DerivativeResult struct {
	Data      []byte
	Key       string
	Cache     string
	Coalesced bool
	// Isolation names the isolation outcome that ended the request early:
	// "shed" for a slot wait limit, "kill-switch" for the operator switch.
	// It is empty for every other outcome.
	Isolation string
}

type ProcessorState struct {
	TransformInflight  int64
	TransformWaiting   int64
	CoordinatorKeys    int
	CoordinatorWaiters int
}

// NewProcessor builds the E1-style processor: the transformer is expected to
// bound its own concurrency and no isolation controls are attached.
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

// NewProcessorWithIsolation builds a processor whose transform concurrency is
// owned by the isolation gate. The transformer should not bound concurrency
// again on its own.
func NewProcessorWithIsolation(
	originals OriginalStore,
	derivatives DerivativeStore,
	transformer Transformer,
	coordinator Coordinator,
	metrics *Metrics,
	isolation Isolation,
) (*Processor, error) {
	if err := isolation.validate(); err != nil {
		return nil, err
	}
	processor := NewProcessor(originals, derivatives, transformer, coordinator, metrics)
	processor.isolation = &isolation
	return processor, nil
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

	if p.isolation != nil && p.isolation.KillSwitch != nil && p.isolation.KillSwitch.Enabled() {
		p.metrics.killSwitchRejected.Add(1)
		return DerivativeResult{Key: key, Cache: "miss", Isolation: IsolationOutcomeKillSwitch}, ErrTransformDisabled
	}

	data, coalesced, err := p.coordinator.Do(ctx, key, func(workCtx context.Context) ([]byte, error) {
		return p.createDerivative(workCtx, sourceHash, key, spec)
	})
	if coalesced {
		p.metrics.coalesced.Add(1)
	}
	if err != nil {
		result := DerivativeResult{Key: key, Cache: "miss", Coalesced: coalesced}
		if errors.Is(err, ErrTransformShed) {
			result.Isolation = IsolationOutcomeShed
		}
		return result, err
	}
	return DerivativeResult{Data: data, Key: key, Cache: "miss", Coalesced: coalesced}, nil
}

func (p *Processor) createDerivative(ctx context.Context, sourceHash, key string, spec TransformSpec) ([]byte, error) {
	if data, found, err := p.derivatives.Get(ctx, key); err != nil {
		return nil, fmt.Errorf("recheck derivative: %w", err)
	} else if found {
		return data, nil
	}

	if p.isolation != nil && p.isolation.acquireBeforeRead() {
		release, err := p.acquireSlot(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
	}

	original, err := p.readOriginal(ctx, sourceHash)
	if err != nil {
		return nil, err
	}
	size := int64(len(original))
	p.metrics.originalBytesInflight.Add(size)
	defer p.metrics.originalBytesInflight.Add(-size)

	if p.isolation != nil && !p.isolation.acquireBeforeRead() {
		release, err := p.acquireSlot(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
	}

	p.metrics.transformStarted()
	started := time.Now()
	data, err := p.transform(ctx, sourceHash, original, spec)
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

func (p *Processor) readOriginal(ctx context.Context, sourceHash string) ([]byte, error) {
	if p.isolation != nil && p.isolation.Faults != nil {
		if err := p.isolation.Faults.beforeOriginalRead(ctx, sourceHash); err != nil {
			p.metrics.originalError.Add(1)
			return nil, fmt.Errorf("read original: %w", err)
		}
	}
	original, err := p.originals.Read(ctx, sourceHash)
	if err != nil {
		p.metrics.originalError.Add(1)
		return nil, fmt.Errorf("read original: %w", err)
	}
	p.metrics.originalSuccess.Add(1)
	return original, nil
}

func (p *Processor) transform(ctx context.Context, sourceHash string, original []byte, spec TransformSpec) ([]byte, error) {
	if p.isolation != nil && p.isolation.Faults != nil {
		if err := p.isolation.Faults.beforeTransform(ctx, sourceHash); err != nil {
			return nil, err
		}
	}
	return p.transformer.Transform(ctx, original, spec)
}

func (p *Processor) acquireSlot(ctx context.Context) (func(), error) {
	started := time.Now()
	release, err := p.isolation.Gate.Acquire(ctx)
	p.metrics.transformWaitNanos.Add(time.Since(started).Nanoseconds())
	p.metrics.transformWaitCount.Add(1)
	if err != nil {
		if errors.Is(err, ErrTransformShed) {
			p.metrics.transformShed.Add(1)
		}
		return nil, fmt.Errorf("acquire transform slot: %w", err)
	}
	return release, nil
}

func (p *Processor) Metrics() *Metrics {
	return p.metrics
}

func (p *Processor) CoordinatorMode() string {
	return p.coordinator.Mode()
}

// Isolation returns the attached isolation controls, or nil for an E1-style
// processor without them.
func (p *Processor) Isolation() *Isolation {
	return p.isolation
}

func (p *Processor) State() ProcessorState {
	state := ProcessorState{TransformInflight: p.metrics.Snapshot().TransformInflight}
	if p.isolation != nil {
		state.TransformWaiting = p.isolation.Gate.Waiting()
	}
	if coordinator, ok := p.coordinator.(interface {
		Snapshot() ProcessCoordinatorSnapshot
	}); ok {
		snapshot := coordinator.Snapshot()
		state.CoordinatorKeys = snapshot.Keys
		state.CoordinatorWaiters = snapshot.Waiters
	}
	return state
}
