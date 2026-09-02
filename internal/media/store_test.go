package media

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestLocalDerivativeStorePublishesOnlyOneCompleteFile(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	store, err := NewLocalDerivativeStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	key := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	values := [][]byte{[]byte("first-complete-value"), []byte("second-complete-value")}

	created := make(chan bool, len(values))
	var wait sync.WaitGroup
	for _, value := range values {
		wait.Add(1)
		go func() {
			defer wait.Done()
			wasCreated, putErr := store.PutIfAbsent(context.Background(), key, value)
			if putErr != nil {
				t.Errorf("PutIfAbsent() error = %v", putErr)
			}
			created <- wasCreated
		}()
	}
	wait.Wait()
	close(created)

	createdCount := 0
	for wasCreated := range created {
		if wasCreated {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("created count = %d, want 1", createdCount)
	}

	data, found, err := store.Get(context.Background(), key)
	if err != nil || !found {
		t.Fatalf("Get() = %q, %v, %v; want data, true, nil", data, found, err)
	}
	if string(data) != string(values[0]) && string(data) != string(values[1]) {
		t.Fatalf("stored partial or unknown value %q", data)
	}

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(filepath.Join(directory, key+".webp")) {
		t.Fatalf("directory entries = %v, want only final derivative", entries)
	}
}
