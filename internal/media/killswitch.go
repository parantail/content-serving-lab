package media

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// ErrTransformDisabled is returned for derivative misses while the operator
// kill switch is enabled. Derivative hits are not affected.
var ErrTransformDisabled = errors.New("transform path disabled by kill switch")

type KillSwitchTransition struct {
	Enabled bool      `json:"enabled"`
	At      time.Time `json:"at"`
}

// KillSwitch is the operator control that turns the transform path off. It
// records every transition so an experiment can align the switch with the
// request timeline.
type KillSwitch struct {
	enabled atomic.Bool
	metrics *Metrics
	now     func() time.Time

	mu          sync.Mutex
	transitions []KillSwitchTransition
}

func NewKillSwitch(metrics *Metrics) *KillSwitch {
	if metrics == nil {
		metrics = &Metrics{}
	}
	return &KillSwitch{metrics: metrics, now: time.Now}
}

func (k *KillSwitch) Enabled() bool {
	return k.enabled.Load()
}

// Set changes the switch and reports whether the state actually changed and
// when the transition was recorded.
func (k *KillSwitch) Set(enabled bool) (changed bool, at time.Time) {
	k.mu.Lock()
	defer k.mu.Unlock()
	at = k.now()
	if k.enabled.Load() == enabled {
		return false, at
	}
	k.enabled.Store(enabled)
	if enabled {
		k.metrics.killSwitchState.Store(1)
	} else {
		k.metrics.killSwitchState.Store(0)
	}
	k.transitions = append(k.transitions, KillSwitchTransition{Enabled: enabled, At: at})
	return true, at
}

func (k *KillSwitch) Transitions() []KillSwitchTransition {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]KillSwitchTransition{}, k.transitions...)
}
