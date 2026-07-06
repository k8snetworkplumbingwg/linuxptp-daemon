package daemon

import (
	"testing"
	"time"
)

const testProcessName = "proc"

func TestDowntimeTracker_RecordAndEvaluate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		threshold    time.Duration
		downtime     time.Duration
		wantExceeded bool
	}{
		{
			name:         "within threshold",
			threshold:    5 * time.Second,
			downtime:     2 * time.Second,
			wantExceeded: false,
		},
		{
			name:         "exactly at threshold",
			threshold:    5 * time.Second,
			downtime:     5 * time.Second,
			wantExceeded: false,
		},
		{
			name:         "exceeds threshold",
			threshold:    5 * time.Second,
			downtime:     6 * time.Second,
			wantExceeded: true,
		},
		{
			name:         "zero threshold always exceeded",
			threshold:    0,
			downtime:     1 * time.Millisecond,
			wantExceeded: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tracker := NewDowntimeTracker(map[string]time.Duration{
				testProcessName: tc.threshold,
			})

			failedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			restartedAt := failedAt.Add(tc.downtime)

			tracker.RecordFailure(testProcessName, failedAt)
			result := tracker.EvaluateRestart(testProcessName, restartedAt)

			if result.ThresholdExceeded != tc.wantExceeded {
				t.Errorf("ThresholdExceeded = %v, want %v (downtime=%v, threshold=%v)",
					result.ThresholdExceeded, tc.wantExceeded, result.Downtime, result.Threshold)
			}
			if result.Downtime != tc.downtime {
				t.Errorf("Downtime = %v, want %v", result.Downtime, tc.downtime)
			}
			if result.ProcessName != testProcessName {
				t.Errorf("ProcessName = %q, want %q", result.ProcessName, testProcessName)
			}
		})
	}
}

func TestDowntimeTracker_NoRecordedFailure(t *testing.T) {
	t.Parallel()
	tracker := NewDowntimeTracker(map[string]time.Duration{testProcessName: 5 * time.Second})
	result := tracker.EvaluateRestart(testProcessName, time.Now())
	if result.ThresholdExceeded {
		t.Error("should not exceed threshold when no failure was recorded")
	}
	if result.Downtime != 0 {
		t.Errorf("Downtime should be 0, got %v", result.Downtime)
	}
}

func TestDowntimeTracker_EvaluateClearsFailure(t *testing.T) {
	t.Parallel()
	tracker := NewDowntimeTracker(map[string]time.Duration{testProcessName: 5 * time.Second})

	failedAt := time.Now()
	tracker.RecordFailure(testProcessName, failedAt)
	tracker.EvaluateRestart(testProcessName, failedAt.Add(1*time.Second))

	result := tracker.EvaluateRestart(testProcessName, failedAt.Add(2*time.Second))
	if result.Downtime != 0 {
		t.Error("second evaluate should find no recorded failure")
	}
}

func TestDowntimeTracker_SetThresholds(t *testing.T) {
	t.Parallel()
	tracker := NewDowntimeTracker(map[string]time.Duration{testProcessName: 5 * time.Second})

	tracker.SetThresholds(map[string]time.Duration{testProcessName: 10 * time.Second})

	if got := tracker.GetThreshold(testProcessName); got != 10*time.Second {
		t.Errorf("threshold = %v, want 10s", got)
	}
}

func TestDowntimeTracker_MultipleProcesses(t *testing.T) {
	t.Parallel()
	tracker := NewDowntimeTracker(map[string]time.Duration{
		"a": 5 * time.Second,
		"b": 1 * time.Second,
	})

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tracker.RecordFailure("a", base)
	tracker.RecordFailure("b", base)

	resultA := tracker.EvaluateRestart("a", base.Add(3*time.Second))
	resultB := tracker.EvaluateRestart("b", base.Add(3*time.Second))

	if resultA.ThresholdExceeded {
		t.Error("process 'a' should be within 5s threshold")
	}
	if !resultB.ThresholdExceeded {
		t.Error("process 'b' should exceed 1s threshold")
	}
}

func TestEventSuppressionGate_SuppressE3Always(t *testing.T) {
	t.Parallel()
	tracker := NewDowntimeTracker(map[string]time.Duration{})
	semantics := map[string]ProcessFailureSemantics{
		orchPtp4lTR: {SuppressE3Always: true},
	}
	gate := NewEventSuppressionGate(tracker, semantics)

	if !gate.ShouldSuppressEvent(orchPtp4lTR, EventTypeOSClockSyncState) {
		t.Error("E3 should always be suppressed for ptp4l-tr")
	}
	if gate.ShouldSuppressEvent(orchPtp4lTR, EventTypePTPStateChange) {
		t.Error("E1 should not be suppressed for ptp4l-tr without EmitE1Always or SilentRestart")
	}
}

func TestEventSuppressionGate_EmitE1Always(t *testing.T) {
	t.Parallel()
	tracker := NewDowntimeTracker(map[string]time.Duration{})
	semantics := map[string]ProcessFailureSemantics{
		orchPtp4lTT: {EmitE1Always: true},
	}
	gate := NewEventSuppressionGate(tracker, semantics)
	gate.OnProcessFailed(orchPtp4lTT)

	if gate.ShouldSuppressEvent(orchPtp4lTT, EventTypePTPStateChange) {
		t.Error("E1 should never be suppressed when EmitE1Always is set")
	}
}

func TestEventSuppressionGate_SilentRestart(t *testing.T) {
	t.Parallel()
	tracker := NewDowntimeTracker(map[string]time.Duration{ts2phcProcessName: 50 * time.Millisecond})
	semantics := map[string]ProcessFailureSemantics{
		ts2phcProcessName: {SilentRestart: true},
	}
	gate := NewEventSuppressionGate(tracker, semantics)

	gate.OnProcessFailed(ts2phcProcessName)
	if !gate.ShouldSuppressEvent(ts2phcProcessName, EventTypePTPStateChange) {
		t.Error("events should be suppressed during silent restart window")
	}

	gate.OnProcessRestarted(ts2phcProcessName, DowntimeResult{ThresholdExceeded: false})
	if !gate.ShouldSuppressEvent(ts2phcProcessName, EventTypePTPStateChange) {
		t.Error("events should remain suppressed after restart within threshold (timer still active)")
	}

	time.Sleep(60 * time.Millisecond)
	if gate.ShouldSuppressEvent(ts2phcProcessName, EventTypePTPStateChange) {
		t.Error("events should not be suppressed after threshold timer expires")
	}
}

func TestEventSuppressionGate_ThresholdExceeded(t *testing.T) {
	t.Parallel()
	tracker := NewDowntimeTracker(map[string]time.Duration{ts2phcProcessName: 5 * time.Second})
	semantics := map[string]ProcessFailureSemantics{
		ts2phcProcessName: {SilentRestart: true},
	}
	gate := NewEventSuppressionGate(tracker, semantics)

	gate.OnProcessFailed(ts2phcProcessName)
	gate.OnProcessRestarted(ts2phcProcessName, DowntimeResult{ThresholdExceeded: true})
	if gate.ShouldSuppressEvent(ts2phcProcessName, EventTypePTPStateChange) {
		t.Error("events should not be suppressed when threshold is exceeded")
	}
}

func TestEventSuppressionGate_UnknownProcess(t *testing.T) {
	t.Parallel()
	tracker := NewDowntimeTracker(map[string]time.Duration{})
	gate := NewEventSuppressionGate(tracker, map[string]ProcessFailureSemantics{})

	if gate.ShouldSuppressEvent("unknown", EventTypePTPStateChange) {
		t.Error("unknown process should not trigger suppression")
	}
}

func TestEventSuppressionGate_ClockClassChange(t *testing.T) {
	t.Parallel()
	tracker := NewDowntimeTracker(map[string]time.Duration{})
	semantics := map[string]ProcessFailureSemantics{
		testProcessName: {SilentRestart: true},
	}
	gate := NewEventSuppressionGate(tracker, semantics)
	gate.OnProcessFailed(testProcessName)

	if !gate.ShouldSuppressEvent(testProcessName, EventTypeClockClassChange) {
		t.Error("clock-class-change should be suppressed during silent restart")
	}
}
