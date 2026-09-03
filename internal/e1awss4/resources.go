package e1awss4

import (
	"context"
	"sync"
	"time"
)

type ResourceSource interface {
	Sample(ctx context.Context) (ResourcePoint, error)
}

type ResourceSample struct {
	Timestamp        string `json:"timestamp"`
	CPUUsageNanos    uint64 `json:"cpu_usage_nanos,omitempty"`
	MemoryUsageBytes uint64 `json:"memory_usage_bytes,omitempty"`
	Error            string `json:"error,omitempty"`
}

type resourceSampler struct {
	source ResourceSource
	gap    time.Duration
	stop   chan struct{}
	done   chan struct{}

	mu      sync.Mutex
	samples []ResourceSample
}

func startResourceSampler(ctx context.Context, source ResourceSource, gap time.Duration) (*resourceSampler, error) {
	point, err := source.Sample(ctx)
	if err != nil {
		return nil, err
	}
	sampler := &resourceSampler{
		source: source,
		gap:    gap,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
		samples: []ResourceSample{{
			Timestamp:        time.Now().UTC().Format(time.RFC3339Nano),
			CPUUsageNanos:    point.CPUUsageNanos,
			MemoryUsageBytes: point.MemoryUsageBytes,
		}},
	}
	go sampler.run()
	return sampler, nil
}

func (s *resourceSampler) run() {
	defer close(s.done)
	ticker := time.NewTicker(s.gap)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.capture()
		case <-s.stop:
			s.capture()
			return
		}
	}
}

func (s *resourceSampler) capture() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	point, err := s.source.Sample(ctx)
	sample := ResourceSample{Timestamp: time.Now().UTC().Format(time.RFC3339Nano)}
	if err != nil {
		sample.Error = err.Error()
	} else {
		sample.CPUUsageNanos = point.CPUUsageNanos
		sample.MemoryUsageBytes = point.MemoryUsageBytes
	}
	s.mu.Lock()
	s.samples = append(s.samples, sample)
	s.mu.Unlock()
}

func (s *resourceSampler) snapshot() []ResourceSample {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ResourceSample(nil), s.samples...)
}

func (s *resourceSampler) finish() []ResourceSample {
	close(s.stop)
	<-s.done
	return s.snapshot()
}
