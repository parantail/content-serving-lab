package media

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// IsolationMode selects how the processor protects healthy requests from
// transform-path failures. The modes are the comparison arms of E3.
type IsolationMode string

const (
	// IsolationBaseline keeps the current behaviour: the original is read
	// before waiting for a transform slot and the wait is bounded only by the
	// transform timeout.
	IsolationBaseline IsolationMode = "baseline"
	// IsolationBoundedWait acquires a transform slot before reading the
	// original and sheds the request once the slot wait limit passes.
	IsolationBoundedWait IsolationMode = "bounded-wait"
	// IsolationKillSwitch behaves like the baseline but exposes an operator
	// switch that fails derivative misses immediately while enabled.
	IsolationKillSwitch IsolationMode = "kill-switch"
)

var isolationModes = []IsolationMode{IsolationBaseline, IsolationBoundedWait, IsolationKillSwitch}

func ParseIsolationMode(raw string) (IsolationMode, error) {
	mode := IsolationMode(strings.ToLower(strings.TrimSpace(raw)))
	for _, known := range isolationModes {
		if mode == known {
			return mode, nil
		}
	}
	return "", fmt.Errorf("unknown isolation mode %q", raw)
}

// Isolation bundles the transform gate, the optional kill switch and the
// optional fault injector for one processor.
type Isolation struct {
	Mode       IsolationMode
	Gate       *TransformGate
	KillSwitch *KillSwitch
	Faults     *FaultInjector
}

// NewIsolation builds the gate and controls for a mode. waitLimit is used
// only by bounded-wait and must be positive there.
func NewIsolation(mode IsolationMode, concurrency int, waitLimit time.Duration, metrics *Metrics) (Isolation, error) {
	if _, err := ParseIsolationMode(string(mode)); err != nil {
		return Isolation{}, err
	}
	isolation := Isolation{Mode: mode}
	var err error
	switch mode {
	case IsolationBoundedWait:
		if waitLimit <= 0 {
			return Isolation{}, errors.New("bounded-wait isolation requires a positive slot wait limit")
		}
		isolation.Gate, err = NewTransformGate(concurrency, waitLimit)
	case IsolationKillSwitch:
		isolation.Gate, err = NewTransformGate(concurrency, 0)
		isolation.KillSwitch = NewKillSwitch(metrics)
	default:
		isolation.Gate, err = NewTransformGate(concurrency, 0)
	}
	if err != nil {
		return Isolation{}, err
	}
	return isolation, nil
}

func (i Isolation) validate() error {
	if _, err := ParseIsolationMode(string(i.Mode)); err != nil {
		return err
	}
	if i.Gate == nil {
		return errors.New("isolation requires a transform gate")
	}
	switch i.Mode {
	case IsolationBoundedWait:
		if i.Gate.WaitLimit() <= 0 {
			return errors.New("bounded-wait isolation requires a gate with a positive wait limit")
		}
		if i.KillSwitch != nil {
			return errors.New("bounded-wait isolation must not carry a kill switch")
		}
	case IsolationKillSwitch:
		if i.KillSwitch == nil {
			return errors.New("kill-switch isolation requires a kill switch")
		}
		if i.Gate.WaitLimit() != 0 {
			return errors.New("kill-switch isolation must not bound the slot wait")
		}
	default:
		if i.KillSwitch != nil {
			return errors.New("baseline isolation must not carry a kill switch")
		}
		if i.Gate.WaitLimit() != 0 {
			return errors.New("baseline isolation must not bound the slot wait")
		}
	}
	return nil
}

func (i Isolation) acquireBeforeRead() bool {
	return i.Mode == IsolationBoundedWait
}
