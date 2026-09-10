//go:build linux && e2integration

package e2

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func init() {
	if os.Getenv("E2_TEST_BLOCKING_CHILD") != "1" {
		return
	}
	var batch Batch
	if err := json.NewDecoder(os.Stdin).Decode(&batch); err != nil {
		os.Exit(2)
	}
	encoder := json.NewEncoder(os.Stdout)
	emit := func(e Event) { e.Batch = batch.ID; e.TimeNS = time.Now().UnixNano(); _ = encoder.Encode(e) }
	emit(Event{Type: "ready"})
	emit(Event{Type: "start", ID: "first"})
	time.Sleep(150 * time.Millisecond)
	emit(Event{Type: "start", ID: "second"})
	// Models a C call that does not cooperate with context cancellation.
	for {
		time.Sleep(time.Second)
	}
}

func TestSupervisorKillsUncooperativeChild(t *testing.T) {
	t.Setenv("E2_TEST_BLOCKING_CHILD", "1")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	start := time.Now()
	result, err := Supervise(context.Background(), Job{"test", Batch{ID: "timeout", Concurrency: 4}}, binary, out, false, 250*time.Millisecond)
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("hard timeout failed: %v", err)
	}
	classifications := map[string]string{}
	for _, e := range result.Terminated {
		classifications[e.ID] = e.Error
	}
	if classifications["first"] != "timeout" || classifications["second"] != "interrupted" {
		t.Fatalf("wrong classifications: %v", classifications)
	}
	if result.WaitRSSBytes <= 0 || result.ExitCode != -1 {
		t.Fatal("child was not reaped with wait4")
	}
	if _, err = os.Stat(filepath.Join(out, "timeout.json")); err != nil {
		t.Fatal("missing failure artifact")
	}
}
