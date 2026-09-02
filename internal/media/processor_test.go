package media

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestProcessorCountsCallsAndCoalesces(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mode              string
		wantTransforms    int64
		wantOriginalReads int64
		wantCoalesced     int64
	}{
		{mode: CoordinatorNone, wantTransforms: 10, wantOriginalReads: 10, wantCoalesced: 0},
		{mode: CoordinatorProcessSingle, wantTransforms: 1, wantOriginalReads: 1, wantCoalesced: 9},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			t.Parallel()
			processor, transformer := newTestProcessor(t, tt.mode, &DeterministicTransformer{
				Delay:  100 * time.Millisecond,
				Output: []byte("webp-result"),
			})
			spec, _ := ParseTransformSpec("width=640,height=640,fit=cover,quality=80", "webp")

			const requests = 10
			start := make(chan struct{})
			results := make(chan error, requests)
			var ready sync.WaitGroup
			ready.Add(requests)
			for range requests {
				go func() {
					ready.Done()
					<-start
					result, err := processor.GetDerivative(context.Background(), testSourceHash, spec)
					if err == nil && string(result.Data) != "webp-result" {
						err = errors.New("unexpected derivative")
					}
					results <- err
				}()
			}
			ready.Wait()
			close(start)
			for range requests {
				if err := <-results; err != nil {
					t.Fatal(err)
				}
			}

			snapshot := processor.Metrics().Snapshot()
			if transformer.Calls() != tt.wantTransforms {
				t.Fatalf("transformer calls = %d, want %d", transformer.Calls(), tt.wantTransforms)
			}
			if snapshot.TransformSuccess != tt.wantTransforms || snapshot.OriginalSuccess != tt.wantOriginalReads {
				t.Fatalf("metrics = %+v", snapshot)
			}
			if snapshot.Coalesced != tt.wantCoalesced {
				t.Fatalf("coalesced = %d, want %d", snapshot.Coalesced, tt.wantCoalesced)
			}
		})
	}
}

func TestProcessorRejectsInvalidSourceBeforeDependencies(t *testing.T) {
	t.Parallel()

	processor, transformer := newTestProcessor(t, CoordinatorNone, &DeterministicTransformer{Output: []byte("unused")})
	spec, _ := ParseTransformSpec("width=640,height=640,fit=cover,quality=80", "webp")
	_, err := processor.GetDerivative(context.Background(), "not-a-hash", spec)
	if !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("error = %v, want ErrInvalidSpec", err)
	}
	if transformer.Calls() != 0 {
		t.Fatalf("transformer calls = %d, want 0", transformer.Calls())
	}
	if snapshot := processor.Metrics().Snapshot(); snapshot != (MetricsSnapshot{}) {
		t.Fatalf("metrics = %+v, want zero", snapshot)
	}
}

func newTestProcessor(t *testing.T, mode string, transformer *DeterministicTransformer) (*Processor, *DeterministicTransformer) {
	t.Helper()
	directory := t.TempDir()
	originalPath := filepath.Join(directory, "original.jpg")
	if err := os.WriteFile(originalPath, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	derivatives, err := NewLocalDerivativeStore(filepath.Join(directory, "derivatives"))
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(mode, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	processor := NewProcessor(
		NewFileOriginalStore(map[string]string{testSourceHash: originalPath}),
		derivatives,
		transformer,
		coordinator,
		&Metrics{},
	)
	return processor, transformer
}
