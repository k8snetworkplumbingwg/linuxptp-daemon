package daemon

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
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

// TestParseServoState locks in the mapping from the raw "sN" servo state
// token linuxptp prints (e.g. in "master offset -1 s2 freq +1 path delay
// 18481") to its numeric value, matching upstream's servo.h enum: s0 =
// SERVO_UNLOCKED, s1 = SERVO_JUMP, s2 = SERVO_LOCKED, s3 = SERVO_LOCKED_STABLE.
func TestParseServoState(t *testing.T) {
	tests := []struct {
		name          string
		servoState    string
		expectedValue float64
		expectedOK    bool
	}{
		{name: "s0 unlocked", servoState: "s0", expectedValue: 0, expectedOK: true},
		{name: "s1 jump", servoState: "s1", expectedValue: 1, expectedOK: true},
		{name: "s2 locked", servoState: "s2", expectedValue: 2, expectedOK: true},
		{name: "s3 locked stable", servoState: "s3", expectedValue: 3, expectedOK: true},
		{name: "empty string is not recognized", servoState: "", expectedOK: false},
		{name: "missing s prefix is not recognized", servoState: "2", expectedOK: false},
		{name: "non-numeric suffix is not recognized", servoState: "sx", expectedOK: false},
		{name: "garbage is not recognized", servoState: "not-a-servo-state", expectedOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, ok := parseServoState(tt.servoState)
			assert.Equal(t, tt.expectedOK, ok)
			if tt.expectedOK {
				assert.Equal(t, tt.expectedValue, value)
			}
		})
	}
}

// Prometheus label keys used by the metrics gauges under test, pulled into
// constants (rather than repeating the raw string literals) to satisfy
// goconst: metrics.go already uses "process"/"node"/"iface" as map keys
// dozens of times each, so any new raw occurrence trips the linter.
const (
	labelProcess = "process"
	labelNode    = "node"
	labelIface   = "iface"
)

// TestUpdateServoStateMetrics verifies that updateServoStateMetrics sets the
// ServoState gauge for recognized "sN" tokens and safely no-ops (without
// panicking or touching the gauge) on empty/unrecognized ones.
func TestUpdateServoStateMetrics(t *testing.T) {
	const (
		process = "ptp4l"
		iface   = "ens1f0"
	)

	updateServoStateMetrics(process, iface, "s2")
	gauge := ServoState.With(map[string]string{labelProcess: process, labelNode: NodeName, labelIface: iface})
	assert.Equal(t, float64(2), testutil.ToFloat64(gauge))

	updateServoStateMetrics(process, iface, "s0")
	assert.Equal(t, float64(0), testutil.ToFloat64(gauge))

	// An unrecognized token must not overwrite the last known good value.
	updateServoStateMetrics(process, iface, "garbage")
	assert.Equal(t, float64(0), testutil.ToFloat64(gauge))
}
