package daemon

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	"github.com/stretchr/testify/assert"
)

// --- ReadyTracker.Ready() unit tests ---

func makeReadyTracker(processes []process.Process) *ReadyTracker {
	return &ReadyTracker{
		config: true,
		processManager: &ProcessManager{
			process: processes,
		},
	}
}

func readyProc(name string, stopped, hasMetrics bool, delayedStart bool) *ptpProcess {
	p := &ptpProcess{
		ExecProcess:         ExecProcess{name: name},
		hasCollectedMetrics: hasMetrics,
	}
	if stopped {
		p.state = process.Stopped
	}

	if delayedStart {
		p.conditions = map[process.Action]process.Condition{
			process.ActionStart: process.OnStateAndOffsetForCount{
				ConfigName: "ts2phc.0.config",
				Source:     event.TS2PHC,
				State:      event.PTP_LOCKED,
				MaxOffset:  1e9,
				Count:      3,
			},
		}
	}
	return p
}

func asProcesses(procs ...*ptpProcess) []process.Process {
	out := make([]process.Process, len(procs))
	for i, p := range procs {
		out[i] = p
	}
	return out
}

func TestReady_NoProcesses(t *testing.T) {
	rt := makeReadyTracker(nil)
	ok, msg := rt.Ready()
	assert.False(t, ok)
	assert.Contains(t, msg, "No processes")
}

func TestReady_AllRunningWithMetrics(t *testing.T) {
	rt := makeReadyTracker(asProcesses(
		readyProc(ptp4lProcessName, false, true, false),
		readyProc(phc2sysProcessName, false, true, false),
	))
	ok, msg := rt.Ready()
	assert.True(t, ok, msg)
}

func TestReady_StoppedProcessReportsNotReady(t *testing.T) {
	rt := makeReadyTracker(asProcesses(
		readyProc(ptp4lProcessName, false, true, false),
		readyProc(phc2sysProcessName, true, false, false),
	))
	ok, msg := rt.Ready()
	assert.False(t, ok)
	assert.Contains(t, msg, "Stopped")
	assert.Contains(t, msg, phc2sysProcessName)
}

func TestReady_DelayedPhc2sysNotReportedAsStopped(t *testing.T) {
	// phc2sys is intentionally delayed (start condition set): the pod
	// should be considered ready without it.
	rt := makeReadyTracker(asProcesses(
		readyProc(ptp4lProcessName, false, true, false),
		readyProc(phc2sysProcessName, true, false, true),
	))
	ok, msg := rt.Ready()
	assert.True(t, ok, msg)
}

func TestReady_DelayedTs2phcNotReportedAsStopped(t *testing.T) {
	rt := makeReadyTracker(asProcesses(
		readyProc(ptp4lProcessName, false, true, false),
		readyProc(ts2phcProcessName, true, false, true),
		readyProc(phc2sysProcessName, true, false, true),
	))
	ok, msg := rt.Ready()
	assert.True(t, ok, msg)
}

func TestReady_NilProcessEntrySkipped(t *testing.T) {
	// nil slots in the process slice must not panic.
	rt := makeReadyTracker([]process.Process{
		readyProc(ptp4lProcessName, false, true, false),
		nil,
	})
	ok, msg := rt.Ready()
	assert.True(t, ok, msg)
}

func TestReady_DelayedHaPhc2sysNotReportedAsStopped(t *testing.T) {
	haPhc2sys := readyProc(phc2sysProcessName, true, false, false)
	haPhc2sys.conditions = map[process.Action]process.Condition{
		process.ActionStart: process.Any{Conditions: []process.Condition{
			process.OnStateAndOffsetForCount{ClockID: "ptp4l.0.config", ConfigName: "ptp4l.0.config", Source: event.PTP4l},
			process.OnStateAndOffsetForCount{ClockID: "ptp4l.1.config", ConfigName: "ptp4l.1.config", Source: event.PTP4l},
		}},
	}
	rt := makeReadyTracker(asProcesses(
		readyProc(ptp4lProcessName, false, true, false),
		haPhc2sys,
	))
	ok, msg := rt.Ready()
	assert.True(t, ok, msg)
}

func TestReady_AllProcessesDelayed(t *testing.T) {
	// If every process has a delayed start condition (e.g. a phc2sys-only HA profile
	// where ptp4l lives in separate profiles), the pod must not report ready.
	rt := makeReadyTracker(asProcesses(
		readyProc(phc2sysProcessName, true, false, true),
	))
	ok, msg := rt.Ready()
	assert.False(t, ok)
	assert.Contains(t, msg, "No processes")
}
