package daemon

import (
	"sync"
	"time"
)

// DowntimeResult is the outcome of evaluating process downtime against its threshold.
type DowntimeResult struct {
	ProcessName       string
	Downtime          time.Duration
	Threshold         time.Duration
	ThresholdExceeded bool
}

// DowntimeTracker tracks per-process failure timestamps and threshold evaluation.
type DowntimeTracker struct {
	mu         sync.Mutex
	thresholds map[string]time.Duration
	failures   map[string]time.Time
}

// NewDowntimeTracker creates a tracker with the given per-process thresholds.
func NewDowntimeTracker(thresholds map[string]time.Duration) *DowntimeTracker {
	t := make(map[string]time.Duration, len(thresholds))
	for k, v := range thresholds {
		t[k] = v
	}
	return &DowntimeTracker{
		thresholds: t,
		failures:   make(map[string]time.Time),
	}
}

// RecordFailure stores the failure timestamp for a process.
func (d *DowntimeTracker) RecordFailure(processName string, failedAt time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failures[processName] = failedAt
}

// EvaluateRestart computes downtime and whether it exceeded the configured threshold.
func (d *DowntimeTracker) EvaluateRestart(processName string, restartedAt time.Time) DowntimeResult {
	d.mu.Lock()
	defer d.mu.Unlock()

	failedAt, recorded := d.failures[processName]
	if !recorded {
		return DowntimeResult{ProcessName: processName}
	}

	downtime := restartedAt.Sub(failedAt)
	threshold := d.thresholds[processName]

	delete(d.failures, processName)

	return DowntimeResult{
		ProcessName:       processName,
		Downtime:          downtime,
		Threshold:         threshold,
		ThresholdExceeded: downtime > threshold,
	}
}

// SetThresholds replaces all thresholds (used when CRD config is updated).
func (d *DowntimeTracker) SetThresholds(thresholds map[string]time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.thresholds = make(map[string]time.Duration, len(thresholds))
	for k, v := range thresholds {
		d.thresholds[k] = v
	}
}

// GetThreshold returns the configured threshold for a process.
func (d *DowntimeTracker) GetThreshold(processName string) time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.thresholds[processName]
}

// Event type constants for suppression gate evaluation.
const (
	EventTypePTPStateChange   = "E1"
	EventTypeOSClockSyncState = "E3"
	EventTypeClockClassChange = "clock-class-change"
)

// EventSuppressionGate determines whether state-change events should be emitted
// based on downtime tracking and per-process failure semantics.
type EventSuppressionGate struct {
	mu         sync.Mutex
	tracker    *DowntimeTracker
	semantics  map[string]ProcessFailureSemantics
	suppressed map[string]bool  // processName → currently within suppression window
	generation map[string]int64 // per-process generation to prevent stale timer clears
}

// NewEventSuppressionGate creates a gate using the given tracker and semantics.
func NewEventSuppressionGate(tracker *DowntimeTracker, semantics map[string]ProcessFailureSemantics) *EventSuppressionGate {
	return &EventSuppressionGate{
		tracker:    tracker,
		semantics:  semantics,
		suppressed: make(map[string]bool),
		generation: make(map[string]int64),
	}
}

// OnProcessFailed marks a process as in suppression window for the full
// threshold duration. Suppression remains active until the threshold timer
// expires, even if the process binary restarts sooner (it needs time to
// reach locked state after starting).
func (g *EventSuppressionGate) OnProcessFailed(processName string) {
	g.mu.Lock()
	g.suppressed[processName] = true
	g.generation[processName]++
	gen := g.generation[processName]
	threshold := g.tracker.GetThreshold(processName)
	g.mu.Unlock()

	go func() {
		time.Sleep(threshold)
		g.mu.Lock()
		if g.generation[processName] == gen {
			g.suppressed[processName] = false
		}
		g.mu.Unlock()
	}()
}

// OnProcessRestarted is called when the process binary restarts. The suppression
// window is NOT cleared here — it remains active for the full threshold duration
// set in OnProcessFailed. This ensures that the brief s0 period after restart
// does not trigger holdover.
func (g *EventSuppressionGate) OnProcessRestarted(processName string, result DowntimeResult) {
	if result.ThresholdExceeded {
		g.mu.Lock()
		g.suppressed[processName] = false
		g.mu.Unlock()
	}
}

// SetSuppressed explicitly sets the suppression state for a process.
func (g *EventSuppressionGate) SetSuppressed(processName string, suppressed bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.suppressed[processName] = suppressed
}

// ShouldSuppressEvent returns true if the event should NOT be emitted.
func (g *EventSuppressionGate) ShouldSuppressEvent(processName string, eventType string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	sem, exists := g.semantics[processName]
	if !exists {
		return false
	}

	switch eventType {
	case EventTypePTPStateChange:
		if sem.EmitE1Always {
			return false
		}
	case EventTypeOSClockSyncState:
		if sem.SuppressE3Always {
			return true
		}
	}

	if sem.SilentRestart && g.suppressed[processName] {
		return true
	}

	return false
}
