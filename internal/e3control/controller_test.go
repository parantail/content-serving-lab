package e3control_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parantail/content-serving-lab/internal/e3control"
	"github.com/parantail/content-serving-lab/internal/media"
)

const (
	sourceHash   = "de206136ed0eeaa42499840f49fa35bd3d57745b8a9580b16f0d6e6393ac7f91"
	poisonedHash = "abababababababababababababababababababababababababababababababab"
)

func newProcessor(t *testing.T, mode media.IsolationMode, withFaults bool) *media.Processor {
	t.Helper()
	directory := t.TempDir()
	originalPath := filepath.Join(directory, "original.jpg")
	if err := os.WriteFile(originalPath, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	derivatives, err := media.NewLocalDerivativeStore(filepath.Join(directory, "derivatives"))
	if err != nil {
		t.Fatal(err)
	}
	metrics := &media.Metrics{}
	isolation, err := media.NewIsolation(mode, 2, time.Second, metrics)
	if err != nil {
		t.Fatal(err)
	}
	if withFaults {
		isolation.Faults, err = media.NewFaultInjector(poisonedHash, metrics)
		if err != nil {
			t.Fatal(err)
		}
	}
	processor, err := media.NewProcessorWithIsolation(
		media.NewFileOriginalStore(map[string]string{sourceHash: originalPath, poisonedHash: originalPath}),
		derivatives,
		&media.DeterministicTransformer{Delay: time.Millisecond, Output: []byte("webp")},
		media.NewProcessCoordinator(time.Second),
		metrics,
		isolation,
	)
	if err != nil {
		t.Fatal(err)
	}
	return processor
}

func TestControllerRequiresIsolation(t *testing.T) {
	t.Parallel()

	legacy := media.NewProcessor(nil, nil, nil, media.NewNoneCoordinator(time.Second), &media.Metrics{})
	if _, err := e3control.NewController(legacy); err == nil {
		t.Fatal("controller accepted a processor without isolation")
	}
	if _, err := e3control.NewController(nil); err == nil {
		t.Fatal("controller accepted a nil processor")
	}
}

func TestControllerStateAndControls(t *testing.T) {
	t.Parallel()

	controller, err := e3control.NewController(newProcessor(t, media.IsolationKillSwitch, true))
	if err != nil {
		t.Fatal(err)
	}

	state := controller.State()
	if state.Mode != string(media.IsolationKillSwitch) || state.Gate.Concurrency != 2 || state.Gate.WaitLimit != "0s" {
		t.Fatalf("state = %+v", state)
	}
	if state.Fault == nil || state.Fault.Kind != "none" || state.Fault.SourceHash != poisonedHash || state.Fault.Since != nil {
		t.Fatalf("fault view = %+v", state.Fault)
	}
	if state.KillSwitch == nil || state.KillSwitch.Enabled || len(state.KillSwitch.Transitions) != 0 {
		t.Fatalf("kill switch view = %+v", state.KillSwitch)
	}

	state, err = controller.SetFault("slow-transform", "15s")
	if err != nil {
		t.Fatal(err)
	}
	if state.Fault.Kind != "slow-transform" || state.Fault.Delay != "15s" || state.Fault.Since == nil {
		t.Fatalf("fault view after set = %+v", state.Fault)
	}

	for _, tt := range []struct{ kind, delay string }{
		{"bogus", ""},
		{"slow-transform", ""},
		{"slow-transform", "soon"},
		{"transform-error", "-1s"},
	} {
		if _, err := controller.SetFault(tt.kind, tt.delay); !errors.Is(err, e3control.ErrInvalidRequest) {
			t.Fatalf("SetFault(%q, %q) error = %v, want ErrInvalidRequest", tt.kind, tt.delay, err)
		}
	}

	state, err = controller.SetKillSwitch(true)
	if err != nil {
		t.Fatal(err)
	}
	if !state.KillSwitch.Enabled || len(state.KillSwitch.Transitions) != 1 || !state.KillSwitch.Transitions[0].Enabled {
		t.Fatalf("kill switch view after enable = %+v", state.KillSwitch)
	}
	if state.Metrics.KillSwitchState != 1 {
		t.Fatalf("metrics snapshot kill switch state = %d, want 1", state.Metrics.KillSwitchState)
	}
}

func TestControllerRejectsUnavailableControls(t *testing.T) {
	t.Parallel()

	controller, err := e3control.NewController(newProcessor(t, media.IsolationBaseline, false))
	if err != nil {
		t.Fatal(err)
	}
	if state := controller.State(); state.Fault != nil || state.KillSwitch != nil {
		t.Fatalf("baseline state exposes controls: %+v", state)
	}
	if _, err := controller.SetFault("slow-transform", "1s"); !errors.Is(err, e3control.ErrFaultsDisabled) {
		t.Fatalf("SetFault error = %v, want ErrFaultsDisabled", err)
	}
	if _, err := controller.SetKillSwitch(true); !errors.Is(err, e3control.ErrKillSwitchDisabled) {
		t.Fatalf("SetKillSwitch error = %v, want ErrKillSwitchDisabled", err)
	}
}
