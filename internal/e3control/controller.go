// Package e3control exposes the E3 experiment controls: the deterministic
// fault injector and the operator kill switch. The controls are opt-in and
// are never mounted in a plain service process.
package e3control

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/parantail/content-serving-lab/internal/media"
)

var (
	ErrFaultsDisabled     = errors.New("fault injection is not configured for this process")
	ErrKillSwitchDisabled = errors.New("kill switch is not available in this isolation mode")
	ErrInvalidRequest     = errors.New("invalid control request")
)

type Controller struct {
	processor *media.Processor
	isolation *media.Isolation
	now       func() time.Time
}

func NewController(processor *media.Processor) (*Controller, error) {
	if processor == nil {
		return nil, errors.New("e3control: processor is required")
	}
	isolation := processor.Isolation()
	if isolation == nil {
		return nil, errors.New("e3control: processor has no isolation controls")
	}
	return &Controller{processor: processor, isolation: isolation, now: time.Now}, nil
}

type FaultView struct {
	Kind       string     `json:"kind"`
	Delay      string     `json:"delay"`
	Since      *time.Time `json:"since,omitempty"`
	SourceHash string     `json:"source_hash"`
}

type KillSwitchView struct {
	Enabled     bool                         `json:"enabled"`
	Transitions []media.KillSwitchTransition `json:"transitions"`
}

type GateView struct {
	Concurrency int    `json:"concurrency"`
	WaitLimit   string `json:"wait_limit"`
	Inflight    int    `json:"inflight"`
	Waiting     int64  `json:"waiting"`
}

type State struct {
	Mode       string                `json:"mode"`
	Time       time.Time             `json:"time"`
	Fault      *FaultView            `json:"fault"`
	KillSwitch *KillSwitchView       `json:"kill_switch"`
	Gate       GateView              `json:"gate"`
	Processor  media.ProcessorState  `json:"processor"`
	Metrics    media.MetricsSnapshot `json:"metrics"`
}

func (c *Controller) Mode() media.IsolationMode {
	return c.isolation.Mode
}

func (c *Controller) State() State {
	state := State{
		Mode:      string(c.isolation.Mode),
		Time:      c.now(),
		Processor: c.processor.State(),
		Metrics:   c.processor.Metrics().Snapshot(),
		Gate: GateView{
			Concurrency: c.isolation.Gate.Concurrency(),
			WaitLimit:   c.isolation.Gate.WaitLimit().String(),
			Inflight:    c.isolation.Gate.Inflight(),
			Waiting:     c.isolation.Gate.Waiting(),
		},
	}
	if c.isolation.Faults != nil {
		fault := c.isolation.Faults.State()
		view := &FaultView{
			Kind:       string(fault.Kind),
			Delay:      fault.Delay.String(),
			SourceHash: c.isolation.Faults.SourceHash(),
		}
		if !fault.Since.IsZero() {
			since := fault.Since
			view.Since = &since
		}
		state.Fault = view
	}
	if c.isolation.KillSwitch != nil {
		state.KillSwitch = &KillSwitchView{
			Enabled:     c.isolation.KillSwitch.Enabled(),
			Transitions: c.isolation.KillSwitch.Transitions(),
		}
	}
	return state
}

// SetFault activates a fault for the poisoned source. delay is a Go duration
// string and may be empty for faults that do not use it.
func (c *Controller) SetFault(kind, delay string) (State, error) {
	if c.isolation.Faults == nil {
		return State{}, ErrFaultsDisabled
	}
	parsedKind, err := media.ParseFaultKind(kind)
	if err != nil {
		return State{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	var parsedDelay time.Duration
	if strings.TrimSpace(delay) != "" {
		parsedDelay, err = time.ParseDuration(strings.TrimSpace(delay))
		if err != nil {
			return State{}, fmt.Errorf("%w: invalid delay %q", ErrInvalidRequest, delay)
		}
	}
	if _, err := c.isolation.Faults.Set(parsedKind, parsedDelay); err != nil {
		return State{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	return c.State(), nil
}

func (c *Controller) SetKillSwitch(enabled bool) (State, error) {
	if c.isolation.KillSwitch == nil {
		return State{}, ErrKillSwitchDisabled
	}
	c.isolation.KillSwitch.Set(enabled)
	return c.State(), nil
}
