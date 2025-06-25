package parser

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGNSSParserInternal(t *testing.T) {
	tests := []struct {
		name           string
		logLine        string
		regex          *regexp.Regexp
		expectedSkip   bool
		expectedResult *gnssParsed
	}{
		{
			name:    "Valid GNSS metrics with status 3",
			regex:   gnssRegex,
			logLine: "gnss[1234.32]:[ts2phc.0.config] ens1f0 gnss_status 3 offset 0 s2",
			expectedResult: &gnssParsed{
				Timestamp:  "1234.32",
				ConfigName: "ts2phc.0.config",
				Interface:  "ens1f0",
				GnssStatus: _ptr(3.0),
				Offset:     _ptr(0.0),
				ServoState: "s2",
			},
		},
		{
			name:    "Valid GNSS metrics with status 0",
			regex:   gnssRegex,
			logLine: "gnss[1234.32]:[ts2phc.0.config] ens1f0 gnss_status 0 offset 0 s0",
			expectedResult: &gnssParsed{
				Timestamp:  "1234.32",
				ConfigName: "ts2phc.0.config",
				Interface:  "ens1f0",
				GnssStatus: _ptr(0.0),
				Offset:     _ptr(0.0),
				ServoState: "s0",
			},
		},
		{
			name:    "Valid GNSS metrics with negative offset",
			regex:   gnssRegex,
			logLine: "gnss[5678.901]:[ts2phc.0.config] ens2f1 gnss_status 1 offset -456 s1",
			expectedResult: &gnssParsed{
				Timestamp:  "5678.901",
				ConfigName: "ts2phc.0.config",
				Interface:  "ens2f1",
				GnssStatus: _ptr(1.0),
				Offset:     _ptr(-456.0),
				ServoState: "s1",
			},
		},
		{
			name:         "Invalid log line",
			regex:        gnssRegex,
			logLine:      "invalid log line",
			expectedSkip: true,
		},
		{
			name:         "Empty log line",
			regex:        gnssRegex,
			logLine:      "",
			expectedSkip: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractor := NewGNSSExtractor()

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
				assert.Equal(t, tt.expectedResult.GnssStatus, res.GnssStatus, "incorrect GnssStatus")
				assert.Equal(t, tt.expectedResult.Offset, res.Offset, "incorrect Offset")
				assert.Equal(t, tt.expectedResult.ServoState, res.ServoState, "incorrect ServoState")
				assert.Equal(t, tt.logLine, res.Raw, "incorrect Raw")
			}
		})
	}
}
