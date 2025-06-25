package parser

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDPLLParserInternal(t *testing.T) {
	tests := []struct {
		name           string
		logLine        string
		regex          *regexp.Regexp
		expectedSkip   bool
		expectedResult *dpllParsed
	}{
		{
			name:    "Valid DPLL metrics with all status values",
			regex:   dpllRegex,
			logLine: "dpll[1700598434]:[ts2phc.0.config] ens2f0 frequency_status 3 offset 0 phase_status 3 pps_status 1 s2",
			expectedResult: &dpllParsed{
				Timestamp:       "1700598434",
				ConfigName:      "ts2phc.0.config",
				Interface:       "ens2f0",
				FrequencyStatus: _ptr(3.0),
				Offset:          _ptr(0.0),
				PhaseStatus:     _ptr(3.0),
				PpsStatus:       _ptr(1.0),
				ServoState:      "s2",
			},
		},
		{
			name:    "Valid DPLL metrics with negative offset",
			regex:   dpllRegex,
			logLine: "dpll[1700598434]:[ts2phc.0.config] ens7f1 frequency_status 1 offset -123 phase_status 2 pps_status 0 s1",
			expectedResult: &dpllParsed{
				Timestamp:       "1700598434",
				ConfigName:      "ts2phc.0.config",
				Interface:       "ens7f1",
				FrequencyStatus: _ptr(1.0),
				Offset:          _ptr(-123.0),
				PhaseStatus:     _ptr(2.0),
				PpsStatus:       _ptr(0.0),
				ServoState:      "s1",
			},
		},
		{
			name:         "Invalid log line",
			regex:        dpllRegex,
			logLine:      "invalid log line",
			expectedSkip: true,
		},
		{
			name:         "Empty log line",
			regex:        dpllRegex,
			logLine:      "",
			expectedSkip: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractor := NewDPLLExtractor()

			res, parsed, err := parseLine(tt.logLine, tt.regex, extractor.NewParsed)
			if err != nil {
				t.Errorf("unexpected error in extraction: %v", err)
			}

			if tt.expectedSkip {
				assert.False(t, parsed)
			} else if tt.expectedResult != nil {
				assert.NotNil(t, res)
				assert.Equal(t, tt.expectedResult.Timestamp, res.Timestamp, "incorrect Timestamp")
				assert.Equal(t, tt.expectedResult.ConfigName, res.ConfigName, "incorrect ConfigName")
				assert.Equal(t, tt.expectedResult.SeverityLevel, res.SeverityLevel, "incorrect SeverityLevel")
				assert.Equal(t, tt.expectedResult.Interface, res.Interface, "incorrect Interface")
				assert.Equal(t, tt.expectedResult.FrequencyStatus, res.FrequencyStatus, "incorrect FrequencyStatus")
				assert.Equal(t, tt.expectedResult.Offset, res.Offset, "incorrect Offset")
				assert.Equal(t, tt.expectedResult.PhaseStatus, res.PhaseStatus, "incorrect PhaseStatus")
				assert.Equal(t, tt.expectedResult.PpsStatus, res.PpsStatus, "incorrect PpsStatus")
				assert.Equal(t, tt.expectedResult.ServoState, res.ServoState, "incorrect ServoState")
				assert.Equal(t, tt.logLine, res.Raw, "incorrect Raw")
			}
		})
	}
}
