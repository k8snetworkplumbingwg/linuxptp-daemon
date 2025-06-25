package parser_test

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/stretchr/testify/assert"
)

func TestPTP4LParser(t *testing.T) {
	tests := []struct {
		name           string
		logLine        string
		configName     string
		expectedError  bool
		expectedMetric *parser.Metrics
	}{
		{
			name:       "Valid summary metrics for master",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[74737.942]: [ptp4l.0.config] rms 53 max 74 freq -16642 +/- 40 delay 1089 +/- 20",
			expectedMetric: &parser.Metrics{
				Iface:      constants.Master,
				Offset:     53,
				MaxOffset:  74,
				FreqAdj:    -16642,
				Delay:      1089,
				ClockState: "",
				Source:     constants.Master,
			},
		},
		{
			name:       "Valid summary metrics for interface",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[5196755.139]: [ptp4l.0.config] ens5f0 rms 3152778 max 3152778 freq -6083928 +/- 0 delay 2791 +/- 0",
			expectedMetric: &parser.Metrics{
				Iface:      "ens5f0",
				Offset:     3152778,
				MaxOffset:  3152778,
				FreqAdj:    -6083928,
				Delay:      2791,
				ClockState: "",
				Source:     constants.Master,
			},
		},
		{
			name:       "Valid regular metrics with master offset",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[365195.391]: [ptp4l.0.config] master offset -1 s2 freq -3972 path delay 89",
			expectedMetric: &parser.Metrics{
				Iface:      constants.Master,
				Offset:     -1,
				MaxOffset:  -1,
				FreqAdj:    -3972,
				Delay:      89,
				ClockState: constants.ClockStateLocked,
				Source:     constants.Master,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractor := parser.NewPTP4LExtractor()

			// Try both summary and regular extraction
			metric, _, err := extractor.Extract(tt.logLine)
			if err != nil && !tt.expectedError {
				t.Errorf("unexpected error in  extraction: %v", err)
			}

			if tt.expectedError {
				assert.NotNil(t, err)
			} else if tt.expectedMetric != nil {
				assert.NotNil(t, metric)
				assert.Equal(t, tt.expectedMetric.Iface, metric.Iface)
				assert.Equal(t, tt.expectedMetric.Offset, metric.Offset)
				assert.Equal(t, tt.expectedMetric.MaxOffset, metric.MaxOffset)
				assert.Equal(t, tt.expectedMetric.FreqAdj, metric.FreqAdj)
				assert.Equal(t, tt.expectedMetric.Delay, metric.Delay)
				assert.Equal(t, tt.expectedMetric.ClockState, metric.ClockState)
				assert.Equal(t, tt.expectedMetric.Source, metric.Source)
			}
		})
	}
}

func TestPTP4LEventParser(t *testing.T) {
	tests := []struct {
		name          string
		logLine       string
		configName    string
		expectedError bool
		expectedEvent *parser.PTPEvent
	}{
		{
			name:       "Port state change to SLAVE",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to SLAVE on MASTER",
			expectedEvent: &parser.PTPEvent{
				PortID: 1,
				Role:   constants.PortRoleSlave,
				Raw:    "ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to SLAVE on MASTER",
			},
		},
		{
			name:       "Port state change to PASSIVE",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to PASSIVE on RS_PASSIVE",
			expectedEvent: &parser.PTPEvent{
				PortID: 1,
				Role:   constants.PortRolePassive,
				Raw:    "ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to PASSIVE on RS_PASSIVE",
			},
		},
		{
			name:       "Port state change to MASTER",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to MASTER on RS_MASTER",
			expectedEvent: &parser.PTPEvent{
				PortID: 1,
				Role:   constants.PortRoleMaster,
				Raw:    "ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to MASTER on RS_MASTER",
			},
		},
		{
			name:       "Port state change to FAULTY",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[4268779.809]: [ptp4l.0.config] port 1: FAULT_DETECTED",
			expectedEvent: &parser.PTPEvent{
				PortID: 1,
				Role:   constants.PortRoleFaulty,
				Raw:    "ptp4l[4268779.809]: [ptp4l.0.config] port 1: FAULT_DETECTED",
			},
		},
		{
			name:       "Port state change to LISTENING",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to LISTENING on RS_LISTENING",
			expectedEvent: &parser.PTPEvent{
				PortID: 1,
				Role:   constants.PortRoleListening,
				Raw:    "ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to LISTENING on RS_LISTENING",
			},
		},
		{
			name:          "Invalid port state change",
			configName:    "ptp4l.0.config",
			logLine:       "ptp4l[4268779.809]: [ptp4l.0.config] port 1: INVALID_STATE",
			expectedError: true,
		},

		// Clock class change event
		{
			name:       "Clock class change event",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[1234.567]: [ptp4l.0.config] CLOCK_CLASS_CHANGE 248",
			expectedEvent: &parser.PTPEvent{
				PortID:     0,
				Role:       constants.PortRoleUnknown,
				ClockClass: 248,
				Raw:        "ptp4l[1234.567]: [ptp4l.0.config] CLOCK_CLASS_CHANGE 248",
			},
		},
		{
			name:       "Clock class change event with severity",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[1234.567]: [ptp4l.0.config:4] CLOCK_CLASS_CHANGE 6",
			expectedEvent: &parser.PTPEvent{
				PortID:     0,
				Role:       constants.PortRoleUnknown,
				ClockClass: 6,
				Raw:        "ptp4l[1234.567]: [ptp4l.0.config:4] CLOCK_CLASS_CHANGE 6",
			},
		},
		{
			name:       "Clock class change to GM class (holdover)",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[5555.111]: [ptp4l.0.config] CLOCK_CLASS_CHANGE 7",
			expectedEvent: &parser.PTPEvent{
				PortID:     0,
				Role:       constants.PortRoleUnknown,
				ClockClass: 7,
				Raw:        "ptp4l[5555.111]: [ptp4l.0.config] CLOCK_CLASS_CHANGE 7",
			},
		},
		{
			name:       "Clock class change to freerun",
			configName: "ptp4l.1.config",
			logLine:    "ptp4l[9999.123]: [ptp4l.1.config:6] CLOCK_CLASS_CHANGE 255",
			expectedEvent: &parser.PTPEvent{
				PortID:     0,
				Role:       constants.PortRoleUnknown,
				ClockClass: 255,
				Raw:        "ptp4l[9999.123]: [ptp4l.1.config:6] CLOCK_CLASS_CHANGE 255",
			},
		},
		{
			name:       "Clock class change to locked state",
			configName: "ptp4l.2.config",
			logLine:    "ptp4l[7777.999]: [ptp4l.2.config] CLOCK_CLASS_CHANGE 135",
			expectedEvent: &parser.PTPEvent{
				PortID:     0,
				Role:       constants.PortRoleUnknown,
				ClockClass: 135,
				Raw:        "ptp4l[7777.999]: [ptp4l.2.config] CLOCK_CLASS_CHANGE 135",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractor := parser.NewPTP4LExtractor()

			_, event, err := extractor.Extract(tt.logLine)
			if tt.expectedError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			if tt.expectedEvent != nil {
				assert.NotNil(t, event)
				assert.Equal(t, tt.expectedEvent.PortID, event.PortID)
				assert.Equal(t, tt.expectedEvent.Role, event.Role)
				assert.Equal(t, tt.expectedEvent.ClockClass, event.ClockClass)
				assert.Equal(t, tt.expectedEvent.Raw, event.Raw)
			}
		})
	}
}
