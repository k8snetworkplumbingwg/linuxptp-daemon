package daemon

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

// TestReplayDualUpstreamLog replays the actual ptp4l log sequence from OCPBUGS-111881
// and verifies openshift_ptp_interface_role metrics after each phase.
//
// Sequence observed on the node (log-dual-upstream.txt):
//
//	Initial boot:
//	  port 1 (eno8303): INITIALIZING → LISTENING → UNCALIBRATED → SLAVE
//	  port 2 (eno8403): INITIALIZING → LISTENING → PRE_MASTER → MASTER
//
//	Switchover (eno8303 link down):
//	  port 1 (eno8303): SLAVE → FAULTY
//	  port 2 (eno8403): MASTER → UNCALIBRATED → [SLAVE if eno8303 doesn't recover fast enough]
//
//	Recovery (eno8303 link up):
//	  port 1 (eno8303): FAULTY → LISTENING → UNCALIBRATED → SLAVE
//	  port 2 (eno8403): SLAVE → PRE_MASTER → MASTER
func TestReplayDualUpstreamLog(t *testing.T) {
	InitializeOffsetMaps()

	process := &ptpProcess{
		ExecProcess: ExecProcess{
			name:       ptp4lProcessName,
			configName: "ptp4l.1.config",
			messageTag: "[ptp4l.1.config]",
			eventCh:    make(chan event.Event, 10),
		},
		ifaces: config.IFaces{
			{Name: "eno8303"},
			{Name: "eno8403"},
		},
		logParser: parser.NewPTP4LExtractor(),
	}

	// --- Phase 1: Initial boot ---
	// ptp4l elects eno8303 as SLAVE, eno8403 becomes MASTER
	bootLines := []string{
		"ptp4l[82484.979]: [ptp4l.1.config:5] port 1 (eno8303): INITIALIZING to LISTENING on INIT_COMPLETE",
		"ptp4l[82484.999]: [ptp4l.1.config:5] port 2 (eno8403): INITIALIZING to LISTENING on INIT_COMPLETE",
		"ptp4l[82485.340]: [ptp4l.1.config:5] port 1 (eno8303): LISTENING to UNCALIBRATED on RS_SLAVE",
		"ptp4l[82485.340]: [ptp4l.1.config:5] port 2 (eno8403): LISTENING to PRE_MASTER on RS_MASTER",
		"ptp4l[82485.590]: [ptp4l.1.config:5] port 2 (eno8403): PRE_MASTER to MASTER on QUALIFICATION_TIMEOUT_EXPIRES",
		"ptp4l[82533.101]: [ptp4l.1.config:5] port 1 (eno8303): UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED",
	}
	for _, line := range bootLines {
		processWithParser(process, line)
	}
	assert.Equal(t, float64(SLAVE), testutil.ToFloat64(InterfaceRole.WithLabelValues(ptp4lProcessName, NodeName, "eno8303")), "boot: eno8303 should be SLAVE")
	assert.Equal(t, float64(MASTER), testutil.ToFloat64(InterfaceRole.WithLabelValues(ptp4lProcessName, NodeName, "eno8403")), "boot: eno8403 should be MASTER")

	// --- Phase 2: Switchover (eno8303 link down) ---
	// eno8403 transitions MASTER → UNCALIBRATED (BMCA re-eval, not FAULTY) → SLAVE
	switchoverLines := []string{
		"ptp4l[82736.439]: [ptp4l.1.config:5] port 1 (eno8303): SLAVE to FAULTY on FAULT_DETECTED (FT_UNSPECIFIED)",
		"ptp4l[82736.000]: [ptp4l.1.config:5] port 2 (eno8403): MASTER to UNCALIBRATED on RS_SLAVE",
		"ptp4l[82737.000]: [ptp4l.1.config:5] port 2 (eno8403): UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED",
	}
	for _, line := range switchoverLines {
		processWithParser(process, line)
	}
	assert.Equal(t, float64(FAULTY), testutil.ToFloat64(InterfaceRole.WithLabelValues(ptp4lProcessName, NodeName, "eno8303")), "switchover: eno8303 should be FAULTY")
	assert.Equal(t, float64(SLAVE), testutil.ToFloat64(InterfaceRole.WithLabelValues(ptp4lProcessName, NodeName, "eno8403")), "switchover: eno8403 should be SLAVE")

	// --- Phase 3: Recovery (eno8303 link up) ---
	// ptp4l logs all transitions; eno8403 goes SLAVE → PRE_MASTER → MASTER as eno8303 takes over
	recoveryLines := []string{
		"ptp4l[82736.439]: [ptp4l.1.config:5] port 1 (eno8303): FAULTY to LISTENING on INIT_COMPLETE",
		"ptp4l[82736.774]: [ptp4l.1.config:5] port 1 (eno8303): LISTENING to UNCALIBRATED on RS_SLAVE",
		"ptp4l[82736.774]: [ptp4l.1.config:5] port 2 (eno8403): SLAVE to PRE_MASTER on RS_MASTER",
		"ptp4l[82737.024]: [ptp4l.1.config:5] port 2 (eno8403): PRE_MASTER to MASTER on QUALIFICATION_TIMEOUT_EXPIRES",
		"ptp4l[82752.903]: [ptp4l.1.config:5] port 1 (eno8303): UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED",
	}
	for _, line := range recoveryLines {
		processWithParser(process, line)
	}
	assert.Equal(t, float64(SLAVE), testutil.ToFloat64(InterfaceRole.WithLabelValues(ptp4lProcessName, NodeName, "eno8303")), "recovery: eno8303 should be SLAVE")
	assert.Equal(t, float64(MASTER), testutil.ToFloat64(InterfaceRole.WithLabelValues(ptp4lProcessName, NodeName, "eno8403")), "recovery: eno8403 should be MASTER")
}

// Test_shouldFreeRun tests the shouldFreeRun function for various offset and state conditions.
// It verifies that the function correctly determines when a process should transition to FREERUN
// based on the current state, offset value, and configured thresholds.
func Test_shouldFreeRun(t *testing.T) {
	threshold100 := &ptpv1.PtpClockThreshold{
		MaxOffsetThreshold: 100,
		MinOffsetThreshold: -100,
	}
	threshold100OmittedMin := &ptpv1.PtpClockThreshold{
		MaxOffsetThreshold: 100,
		MinOffsetThreshold: 0,
	}
	threshold100AsymmetricMin := &ptpv1.PtpClockThreshold{
		MaxOffsetThreshold: 100,
		MinOffsetThreshold: -50,
	}

	tests := []struct {
		name         string
		currentState event.PTPState
		offset       float64
		threshold    *ptpv1.PtpClockThreshold
		expected     bool
	}{
		{
			name:         "already in HOLDOVER -> false",
			currentState: event.PTP_HOLDOVER,
			offset:       500,
			threshold:    threshold100,
			expected:     false,
		},
		{
			name:         "already in FREERUN -> false",
			currentState: event.PTP_FREERUN,
			offset:       500,
			threshold:    threshold100,
			expected:     false,
		},
		{
			name:         "in-range positive offset -> false",
			currentState: event.PTP_LOCKED,
			offset:       50,
			threshold:    threshold100,
			expected:     false,
		},
		{
			name:         "in-range negative offset -> false",
			currentState: event.PTP_LOCKED,
			offset:       -50,
			threshold:    threshold100,
			expected:     false,
		},
		{
			name:         "out-of-range positive offset -> true",
			currentState: event.PTP_LOCKED,
			offset:       150,
			threshold:    threshold100,
			expected:     true,
		},
		{
			name:         "out-of-range negative offset -> true",
			currentState: event.PTP_LOCKED,
			offset:       -150,
			threshold:    threshold100,
			expected:     true,
		},
		{
			name:         "exact positive boundary offset (non-inclusive) -> true",
			currentState: event.PTP_LOCKED,
			offset:       100,
			threshold:    threshold100,
			expected:     true,
		},
		{
			name:         "exact negative boundary offset (non-inclusive) -> true",
			currentState: event.PTP_LOCKED,
			offset:       -100,
			threshold:    threshold100,
			expected:     true,
		},
		{
			name:         "backward-compat: omitted MinOffsetThreshold in-range -> false",
			currentState: event.PTP_LOCKED,
			offset:       50,
			threshold:    threshold100OmittedMin,
			expected:     false,
		},
		{
			name:         "backward-compat: omitted MinOffsetThreshold out-of-range negative -> true",
			currentState: event.PTP_LOCKED,
			offset:       -150,
			threshold:    threshold100OmittedMin,
			expected:     true,
		},
		{
			name:         "backward-compat: asymmetric MinOffsetThreshold behaves identically using abs(offset) < Max -> false",
			currentState: event.PTP_LOCKED,
			offset:       -70,
			threshold:    threshold100AsymmetricMin,
			expected:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := shouldFreeRun(tt.currentState, tt.offset, tt.threshold)
			assert.Equal(t, tt.expected, actual, "shouldFreeRun result mismatch")
		})
	}
}
