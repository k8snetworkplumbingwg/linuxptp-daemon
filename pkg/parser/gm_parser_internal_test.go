package parser

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGMParserInternal(t *testing.T) {
	tests := []struct {
		name           string
		logLine        string
		regex          *regexp.Regexp
		expectedSkip   bool
		expectedResult *gmParsed
	}{
		{
			name:    "Valid GM metrics with s0 state",
			regex:   gmRegex,
			logLine: "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s0",
			expectedResult: &gmParsed{
				Timestamp:  "0",
				ConfigName: "ts2phc.0.config",
				Interface:  "ens1f0",
				GmStatus:   "s0",
			},
		},
		{
			name:    "Valid GM metrics with s2 state",
			regex:   gmRegex,
			logLine: "GM[1234.567]:[ts2phc.0.config] ens2f1 T-GM-STATUS s2",
			expectedResult: &gmParsed{
				Timestamp:  "1234.567",
				ConfigName: "ts2phc.0.config",
				Interface:  "ens2f1",
				GmStatus:   "s2",
			},
		},
		{
			name:         "Invalid log line",
			regex:        gmRegex,
			logLine:      "invalid log line",
			expectedSkip: true,
		},
		{
			name:         "Empty log line",
			regex:        gmRegex,
			logLine:      "",
			expectedSkip: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractor := NewGMExtractor()

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
				assert.Equal(t, tt.expectedResult.GmStatus, res.GmStatus, "incorrect GmStatus")
				assert.Equal(t, tt.logLine, res.Raw, "incorrect Raw")
			}
		})
	}
}
