package parser_test

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/stretchr/testify/assert"
)

func TestGNSSParser(t *testing.T) {
	tests := []struct {
		name           string
		logLine        string
		expectedError  bool
		expectedMetric *parser.Metrics
	}{
		{
			name:    "Valid GNSS metrics with status 3",
			logLine: "gnss[1234.32]:[ts2phc.0.config] ens1f0 gnss_status 3 offset 0 s2",
			expectedMetric: &parser.Metrics{
				Iface:      "ens1f0",
				Offset:     0,
				MaxOffset:  0,
				ClockState: constants.ClockStateLocked,
				Source:     constants.GNSS,
				Status: []parser.StatusMetric{
					{Subtype: "gnss_status", Status: 3.0},
				},
			},
		},
		{
			name:    "Valid GNSS metrics with status 0",
			logLine: "gnss[1234.32]:[ts2phc.0.config] ens1f0 gnss_status 0 offset 0 s0",
			expectedMetric: &parser.Metrics{
				Iface:      "ens1f0",
				Offset:     0,
				MaxOffset:  0,
				ClockState: constants.ClockStateFreeRun,
				Source:     constants.GNSS,
				Status: []parser.StatusMetric{
					{Subtype: "gnss_status", Status: 0.0},
				},
			},
		},
		{
			name:    "Valid GNSS metrics with negative offset",
			logLine: "gnss[5678.901]:[ts2phc.0.config] ens2f1 gnss_status 1 offset -456 s1",
			expectedMetric: &parser.Metrics{
				Iface:      "ens2f1",
				Offset:     -456,
				MaxOffset:  -456,
				ClockState: constants.ClockStateFreeRun,
				Source:     constants.GNSS,
				Status: []parser.StatusMetric{
					{Subtype: "gnss_status", Status: 1.0},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractor := parser.NewGNSSExtractor()

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
				assert.Equal(t, constants.GNSS, metric.From)
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
