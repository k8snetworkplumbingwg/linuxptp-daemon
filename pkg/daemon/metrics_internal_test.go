package daemon

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

func TestProcessStatusHelperDoesNotSetMetric(t *testing.T) {
	beforeStatus := testutil.CollectAndCount(ProcessStatus)
	beforeRestart := testutil.CollectAndCount(ProcessRestartCount)

	processStatus("ptp4l", "[ptp4l.0.config]", PtpProcessUp)

	assert.Equal(t, beforeStatus, testutil.CollectAndCount(ProcessStatus),
		"processStatus helper must not write openshift_ptp_process_status; ClockManager owns the gauge")
	assert.Equal(t, beforeRestart, testutil.CollectAndCount(ProcessRestartCount),
		"processStatus helper must not increment process_restart_count")
}

func TestUpdateClockStateMetrics(t *testing.T) {
	origNodeName := NodeName
	defer func() { NodeName = origNodeName }()

	NodeName = "test-node"
	RegisterMetrics(NodeName)
	ClockState.Reset()

	tests := []struct {
		state    string
		expected float64
	}{
		{FREERUN, event.ClockStateFreerun},
		{LOCKED, event.ClockStateLocked},
		{HOLDOVER, event.ClockStateHoldover},
	}

	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			updateClockStateMetrics(ptp4lProcessName, testDUTLeadingIface, tt.state, "")
			gauge, err := ClockState.GetMetricWith(prometheus.Labels{
				labelProcess: ptp4lProcessName, labelNode: NodeName, labelIface: testDUTLeadingIface,
			})
			assert.NoError(t, err)
			actual := testutil.ToFloat64(gauge)
			assert.Equal(t, tt.expected, actual,
				"clock_state for %s should be %v", tt.state, tt.expected)
		})
	}
}
