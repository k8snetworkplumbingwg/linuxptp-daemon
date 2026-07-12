package daemon

import (
	"fmt"
	"sync"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
)

// StartupPrecondition gates process startup in the T-BC orchestrator.
// Implementations determine when a process is allowed to start.
type StartupPrecondition interface {
	Satisfied() bool
	Name() string
}

// BCStateObserver provides the current T-BC FSM state for precondition evaluation.
type BCStateObserver interface {
	CurrentState() event.PTPState
}

// ProcessStateProvider reports whether a named process is currently running.
type ProcessStateProvider interface {
	IsRunning(name string) bool
}

// PTPSourceStateProvider provides servo state and offset data
// from the ptp4l[TR] process for PTPSourceQualified evaluation.
type PTPSourceStateProvider interface {
	ServoState() event.PTPState
	CurrentOffset() float64
}

// NoPrecondition always returns true. Used for ptp4l[TR] which has no gate.
type NoPrecondition struct{}

// Satisfied implements StartupPrecondition.
func (n *NoPrecondition) Satisfied() bool { return true }

// Name implements StartupPrecondition.
func (n *NoPrecondition) Name() string { return "none" }

// PTPSourceQualifiedPrecondition evaluates whether the PTP source
// is qualified: servo locked (s2) and offset below threshold for
// a configurable number of consecutive samples.
type PTPSourceQualifiedPrecondition struct {
	mu               sync.Mutex
	stateProvider    PTPSourceStateProvider
	offsetThreshold  float64
	requiredSamples  int
	consecutiveCount int
}

// NewPTPSourceQualifiedPrecondition creates a PTPSourceQualified precondition.
// offsetThreshold is in nanoseconds; requiredSamples is how many consecutive
// good readings are needed before qualification.
func NewPTPSourceQualifiedPrecondition(
	provider PTPSourceStateProvider,
	offsetThreshold float64,
	requiredSamples int,
) *PTPSourceQualifiedPrecondition {
	return &PTPSourceQualifiedPrecondition{
		stateProvider:   provider,
		offsetThreshold: offsetThreshold,
		requiredSamples: requiredSamples,
	}
}

// Satisfied implements StartupPrecondition.
func (p *PTPSourceQualifiedPrecondition) Satisfied() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stateProvider.ServoState() != event.PTP_LOCKED {
		p.consecutiveCount = 0
		return false
	}

	offset := p.stateProvider.CurrentOffset()
	if offset < 0 {
		offset = -offset
	}
	if offset > p.offsetThreshold {
		p.consecutiveCount = 0
		return false
	}

	p.consecutiveCount++
	return p.consecutiveCount >= p.requiredSamples
}

// Name implements StartupPrecondition.
func (p *PTPSourceQualifiedPrecondition) Name() string {
	return fmt.Sprintf("PTPSourceQualified(threshold=%.0fns, samples=%d)", p.offsetThreshold, p.requiredSamples)
}

// ResetCount resets the consecutive sample counter. Used when state regresses.
func (p *PTPSourceQualifiedPrecondition) ResetCount() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.consecutiveCount = 0
}

// ProcessRunningPrecondition checks that a named process is in Running state.
type ProcessRunningPrecondition struct {
	processName   string
	stateProvider ProcessStateProvider
}

// NewProcessRunningPrecondition creates a precondition that checks if a process is running.
func NewProcessRunningPrecondition(processName string, provider ProcessStateProvider) *ProcessRunningPrecondition {
	return &ProcessRunningPrecondition{
		processName:   processName,
		stateProvider: provider,
	}
}

// Satisfied implements StartupPrecondition.
func (p *ProcessRunningPrecondition) Satisfied() bool {
	return p.stateProvider.IsRunning(p.processName)
}

// Name implements StartupPrecondition.
func (p *ProcessRunningPrecondition) Name() string {
	return fmt.Sprintf("ProcessRunning(%s)", p.processName)
}

// BCStatePrecondition is satisfied when the T-BC FSM reaches PTP_LOCKED (S2).
type BCStatePrecondition struct {
	observer BCStateObserver
}

// NewBCStatePrecondition creates a precondition gated on T-BC FSM reaching LOCKED (S2).
func NewBCStatePrecondition(observer BCStateObserver) *BCStatePrecondition {
	return &BCStatePrecondition{observer: observer}
}

// Satisfied implements StartupPrecondition.
func (b *BCStatePrecondition) Satisfied() bool {
	return b.observer.CurrentState() == event.PTP_LOCKED
}

// Name implements StartupPrecondition.
func (b *BCStatePrecondition) Name() string {
	return "BCState(S2/LOCKED)"
}

// CompositePrecondition requires ALL sub-preconditions to be satisfied (AND logic).
type CompositePrecondition struct {
	preconditions []StartupPrecondition
}

// NewCompositePrecondition creates a precondition requiring ALL sub-preconditions (AND logic).
func NewCompositePrecondition(preconditions ...StartupPrecondition) *CompositePrecondition {
	return &CompositePrecondition{preconditions: preconditions}
}

// Satisfied implements StartupPrecondition.
func (c *CompositePrecondition) Satisfied() bool {
	for _, p := range c.preconditions {
		if !p.Satisfied() {
			return false
		}
	}
	return true
}

// Name implements StartupPrecondition.
func (c *CompositePrecondition) Name() string {
	names := make([]string, len(c.preconditions))
	for i, p := range c.preconditions {
		names[i] = p.Name()
	}
	return fmt.Sprintf("Composite(%v)", names)
}

// TimedPrecondition wraps a precondition with a timeout. After the timeout
// elapses, Satisfied() returns true regardless of the inner precondition state.
// This prevents indefinite blocking during startup.
type TimedPrecondition struct {
	inner     StartupPrecondition
	timeout   time.Duration
	startTime time.Time
	started   bool
}

// NewTimedPrecondition creates a precondition that falls through after timeout.
func NewTimedPrecondition(inner StartupPrecondition, timeout time.Duration) *TimedPrecondition {
	return &TimedPrecondition{
		inner:   inner,
		timeout: timeout,
	}
}

// Satisfied implements StartupPrecondition.
func (t *TimedPrecondition) Satisfied() bool {
	if !t.started {
		t.startTime = time.Now()
		t.started = true
	}
	if t.inner.Satisfied() {
		return true
	}
	return time.Since(t.startTime) >= t.timeout
}

// Name implements StartupPrecondition.
func (t *TimedPrecondition) Name() string {
	return fmt.Sprintf("Timed(%s, timeout=%s)", t.inner.Name(), t.timeout)
}

// TimedOut returns true if the precondition was satisfied by timeout rather than
// the inner condition becoming true.
func (t *TimedPrecondition) TimedOut() bool {
	return t.started && !t.inner.Satisfied() && time.Since(t.startTime) >= t.timeout
}
