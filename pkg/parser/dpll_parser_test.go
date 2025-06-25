package parser_test

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/stretchr/testify/assert"
)

func TestDPLLParser(t *testing.T) {
	tests := []struct {
		name           string
		logLine        string
		expectedError  bool
		expectedMetric *parser.Metrics
	}{
		{
			name:    "Valid DPLL metrics with all status values",
			logLine: "dpll[1700598434]:[ts2phc.0.config] ens2f0 frequency_status 3 offset 0 phase_status 3 pps_status 1 s2",
			expectedMetric: &parser.Metrics{
				Iface:      "ens2f0",
				Offset:     0,
				MaxOffset:  0,
				ClockState: constants.ClockStateLocked,
				Source:     constants.DPLL,
				Status: []parser.StatusMetric{
					{Subtype: "frequency_status", Status: 3.0},
					{Subtype: "phase_status", Status: 3.0},
					{Subtype: "pps_status", Status: 1.0},
				},
			},
		},
		{
			name:    "Valid DPLL metrics with negative offset",
			logLine: "dpll[1700598434]:[ts2phc.0.config] ens7f1 frequency_status 1 offset -123 phase_status 2 pps_status 0 s1",
			expectedMetric: &parser.Metrics{
				Iface:      "ens7f1",
				Offset:     -123,
				MaxOffset:  -123,
				ClockState: constants.ClockStateFreeRun,
				Source:     constants.DPLL,
				Status: []parser.StatusMetric{
					{Subtype: "frequency_status", Status: 1.0},
					{Subtype: "phase_status", Status: 2.0},
					{Subtype: "pps_status", Status: 0.0},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractor := parser.NewDPLLExtractor()

			metric, _, err := extractor.Extract(tt.logLine)
			if err != nil && !tt.expectedError {
				t.Errorf("unexpected error in extraction: %v", err)
			}

			if tt.expectedError {
				assert.NotNil(t, err)
			} else if tt.expectedMetric != nil {
				assert.NotNil(t, metric)
				assert.Equal(t, tt.expectedMetric.Iface, metric.Iface)
				assert.Equal(t, tt.expectedMetric.Offset, metric.Offset)
				assert.Equal(t, tt.expectedMetric.MaxOffset, metric.MaxOffset)
				assert.Equal(t, tt.expectedMetric.ClockState, metric.ClockState)
				assert.Equal(t, tt.expectedMetric.Source, metric.Source)
				assert.Equal(t, tt.expectedMetric.Status, metric.Status)
				assert.Equal(t, constants.DPLL, metric.From)
				for _, expectedStatus := range tt.expectedMetric.Status {
					found := false
					for _, status := range metric.Status {
						if status.Subtype == expectedStatus.Subtype {
							found = true
							assert.Equal(t, expectedStatus.Status, status.Status, "incorrect Status")
						}
					}
					assert.True(t, found, "Status not found")
				}
			}
		})
	}
}
