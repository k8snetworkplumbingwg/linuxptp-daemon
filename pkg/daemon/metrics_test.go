package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestExtractPTP4lEventState locks in the behavior of extractPTP4lEventState,
// which delegates to the shared ptp4l parser (parser.NewPTP4LExtractor) and
// convertParserRoleToMetricsRole instead of the hand-rolled string
// splitting/matching it used to do. This code path is currently unreachable
// for real ptp4l processes (getParser always returns a non-nil extractor for
// ptp4lProcessName, so processPTPMetrics never falls through to this
// function's caller), but it's pinned here so any future change that makes
// it reachable again doesn't silently regress.
func TestExtractPTP4lEventState(t *testing.T) {
	tests := []struct {
		name         string
		output       string
		expectedID   int
		expectedRole ptpPortRole
	}{
		{
			name:         "locked to SLAVE",
			output:       "ptp4l[123.456]: [test-config.0.config] port 1 (ens4f0): UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED",
			expectedID:   1,
			expectedRole: SLAVE,
		},
		{
			name:         "cold-start BMCA self-election to MASTER",
			output:       "ptp4l[123.456]: [test-config.0.config] port 1 (ens4f0): UNCALIBRATED to MASTER on RS_MASTER",
			expectedID:   1,
			expectedRole: MASTER,
		},
		{
			name:         "departure to PASSIVE",
			output:       "ptp4l[123.456]: [test-config.0.config] port 2 (ens4f1): SLAVE to PASSIVE on RS_PASSIVE",
			expectedID:   2,
			expectedRole: PASSIVE,
		},
		{
			name:         "fault detected",
			output:       "ptp4l[123.456]: [test-config.0.config] port 1 (ens4f0): FAULT_DETECTED",
			expectedID:   1,
			expectedRole: FAULTY,
		},
		{
			name:         "listening",
			output:       "ptp4l[123.456]: [test-config.0.config] port 1 (ens4f0): INITIALIZING to LISTENING",
			expectedID:   1,
			expectedRole: LISTENING,
		},
		{
			name:         "unrecognized event yields UNKNOWN role and zeroed port ID",
			output:       "ptp4l[123.456]: [test-config.0.config] port 1 (ens4f0): delay timeout",
			expectedID:   0,
			expectedRole: UNKNOWN,
		},
		{
			name:         "non-event line (metrics summary) yields UNKNOWN role and zeroed port ID",
			output:       "ptp4l[123.456]: [test-config.0.config] master offset -1 s2 freq -3972 path delay 89",
			expectedID:   0,
			expectedRole: UNKNOWN,
		},
		{
			name:         "garbage input does not panic and yields UNKNOWN role",
			output:       "not a ptp4l line at all",
			expectedID:   0,
			expectedRole: UNKNOWN,
		},
		{
			name:         "empty input does not panic and yields UNKNOWN role",
			output:       "",
			expectedID:   0,
			expectedRole: UNKNOWN,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			portID, role := extractPTP4lEventState(tt.output)
			assert.Equal(t, tt.expectedID, portID)
			assert.Equal(t, tt.expectedRole, role)
		})
	}
}
