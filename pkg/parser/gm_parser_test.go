package parser_test

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/stretchr/testify/assert"
)

func TestGMParser(t *testing.T) {
	tests := []struct {
		name           string
		logLine        string
		expectedError  bool
		expectedMetric *parser.Metrics
	}{
		{
			name:    "Valid GM metrics with s0 state",
			logLine: "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s0",
			expectedMetric: &parser.Metrics{
				Iface:      "ens1f0",
				ClockState: constants.ClockStateFreeRun,
				Source:     constants.GM,
			},
		},
		{
			name:    "Valid GM metrics with holdover",
			logLine: "GM[5678.901]:[ts2phc.0.config] ens2f1 T-GM-STATUS s1",
			expectedMetric: &parser.Metrics{
				Iface:      "ens2f1",
				ClockState: constants.ClockStateFreeRun,
				Source:     constants.GM,
			},
		},
		{
			name:    "Valid GM metrics with s2 state",
			logLine: "GM[1234.567]:[ts2phc.0.config] ens2f1 T-GM-STATUS s2",
			expectedMetric: &parser.Metrics{
				Iface:      "ens2f1",
				ClockState: constants.ClockStateLocked,
				Source:     constants.GM,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractor := parser.NewGMExtractor()

			metric, _, err := extractor.Extract(tt.logLine)
			if err != nil && !tt.expectedError {
				t.Errorf("unexpected error in extraction: %v", err)
			}

			if tt.expectedError {
				assert.NotNil(t, err)
			} else if tt.expectedMetric != nil {
				assert.NotNil(t, metric)
				assert.Equal(t, tt.expectedMetric.Iface, metric.Iface)
				assert.Equal(t, tt.expectedMetric.ClockState, metric.ClockState)
				assert.Equal(t, tt.expectedMetric.Source, metric.Source)
				assert.Equal(t, constants.GM, metric.From)
			}
		})
	}
}
