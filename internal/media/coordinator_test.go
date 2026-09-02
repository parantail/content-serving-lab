package media

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProcessCoordinatorCoalescesSameKey(t *testing.T) {
	t.Parallel()

	coordinator := NewProcessCoordinator(time.Second)
	var calls atomic.Int64
	start := make(chan struct{})
	work := func(context.Context) ([]byte, error) {
		calls.Add(1)
		<-start
		return []byte("result"), nil
	}

	const requestCount = 20
	results := make(chan bool, requestCount)
	var ready sync.WaitGroup
	ready.Add(requestCount)
	for range requestCount {
		go func() {
			ready.Done()
			data, coalesced, err := coordinator.Do(context.Background(), "same-key", work)
			if err != nil || string(data) != "result" {
				t.Errorf("Do() = %q, %v; want result, nil", data, err)
			}
			results <- coalesced
		}()
	}
	ready.Wait()
	waitFor(t, time.Second, func() bool {
		snapshot := coordinator.Snapshot()
		return calls.Load() == 1 && snapshot.Keys == 1 && snapshot.Waiters == requestCount-1
	})
	close(start)

	coalescedCount := 0
	for range requestCount {
		if <-results {
			coalescedCount++
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
	if coalescedCount != requestCount-1 {
		t.Fatalf("coalesced = %d, want %d", coalescedCount, requestCount-1)
	}
}

func TestProcessCoordinatorDoesNotBlockDifferentKeys(t *testing.T) {
	t.Parallel()

	coordinator := NewProcessCoordinator(time.Second)
	started := make(chan string, 2)
	release := make(chan struct{})
	work := func(key string) Work {
		return func(context.Context) ([]byte, error) {
			started <- key
			<-release
			return []byte(key), nil
		}
	}

	for _, key := range []string{"first", "second"} {
		go func() {
			_, _, _ = coordinator.Do(context.Background(), key, work(key))
		}()
	}

	seen := map[string]bool{}
	for range 2 {
		select {
		case key := <-started:
			seen[key] = true
		case <-time.After(time.Second):
			t.Fatal("different key did not start independently")
		}
	}
	close(release)
	if !seen["first"] || !seen["second"] {
		t.Fatalf("started keys = %v", seen)
	}
}

func TestProcessCoordinatorLeaderCancellationDoesNotCancelSharedWork(t *testing.T) {
	t.Parallel()

	coordinator := NewProcessCoordinator(time.Second)
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	workStarted := make(chan struct{})
	release := make(chan struct{})
	work := func(context.Context) ([]byte, error) {
		close(workStarted)
		<-release
		return []byte("result"), nil
	}

	leaderResult := make(chan error, 1)
	go func() {
		_, _, err := coordinator.Do(leaderCtx, "key", work)
		leaderResult <- err
	}()
	<-workStarted

	waiterResult := make(chan error, 1)
	go func() {
		data, coalesced, err := coordinator.Do(context.Background(), "key", work)
		if err == nil && (!coalesced || string(data) != "result") {
			err = errors.New("waiter did not receive the shared result")
		}
		waiterResult <- err
	}()
	waitFor(t, time.Second, func() bool {
		snapshot := coordinator.Snapshot()
		return snapshot.Keys == 1 && snapshot.Waiters == 1
	})

	cancelLeader()
	if err := <-leaderResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader error = %v, want context.Canceled", err)
	}
	close(release)
	if err := <-waiterResult; err != nil {
		t.Fatal(err)
	}
}

func TestProcessCoordinatorSnapshotAggregatesKeysAndWaiters(t *testing.T) {
	t.Parallel()

	coordinator := NewProcessCoordinator(time.Second)
	release := make(chan struct{})
	work := func(context.Context) ([]byte, error) {
		<-release
		return []byte("result"), nil
	}

	for _, key := range []string{"first", "second"} {
		go func() { _, _, _ = coordinator.Do(context.Background(), key, work) }()
		go func() { _, _, _ = coordinator.Do(context.Background(), key, work) }()
	}
	waitFor(t, time.Second, func() bool {
		snapshot := coordinator.Snapshot()
		return snapshot.Keys == 2 && snapshot.Waiters == 2
	})
	close(release)
}

func TestProcessCoordinatorCleansUpAfterFailure(t *testing.T) {
	t.Parallel()

	coordinator := NewProcessCoordinator(time.Second)
	wantErr := errors.New("transform failed")
	_, _, err := coordinator.Do(context.Background(), "key", func(context.Context) ([]byte, error) {
		return nil, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("first error = %v, want %v", err, wantErr)
	}

	data, coalesced, err := coordinator.Do(context.Background(), "key", func(context.Context) ([]byte, error) {
		return []byte("recovered"), nil
	})
	if err != nil || coalesced || string(data) != "recovered" {
		t.Fatalf("second call = %q, %v, %v; want recovered, false, nil", data, coalesced, err)
	}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not met before timeout")
		}
		time.Sleep(time.Millisecond)
	}
}
