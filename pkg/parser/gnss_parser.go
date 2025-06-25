package parser

import (
	"errors"
	"regexp"
	"strconv"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
)

// gnss[1234.32]:[ts2phc.0.config] ens1f0 gnss_status 3 offset 0 s2
// gnss[1234.32]:[ts2phc.0.config] ens1f0 gnss_status 0 offset 0 s0
var gnssRegex = regexp.MustCompile(
	`^gnss\[(?P<timestamp>\d+\.?\d*)\]:` +
		`\s*\[(?P<config_name>.*\.\d+\.c.*g)\]` +
		`\s+(?P<interface>\w+)` +
		`\s+gnss_status\s+(?P<gnss_status>[0-9]+)` +
		`\s+offset\s+(?P<offset>-?\d+)` +
		`\s+(?P<servo_state>s\d+)$`,
)

type gnssParsed struct {
	Raw           string
	Timestamp     string
	ConfigName    string
	SeverityLevel *int
	Interface     string
	GnssStatus    *float64
	Offset        *float64
	ServoState    string
}

// Populate populates the gnssParsed struct from regex matches
func (p *gnssParsed) Populate(line string, matched, fields []string) error {
	p.Raw = line
	for i, field := range fields {
		switch field {
		case constants.Timestamp:
			p.Timestamp = matched[i]
		case constants.ConfigName:
			p.ConfigName = matched[i]
		case constants.Severity:
			if matched[i] == "" { // severity is optional
				continue
			}
			severityLevel, err := strconv.Atoi(matched[i])
			if err != nil {
				return err
			}
			p.SeverityLevel = &severityLevel
		case constants.Interface:
			p.Interface = matched[i]
		case "gnss_status":
			if matched[i] != "" {
				value, err := strconv.ParseFloat(matched[i], 64)
				if err != nil {
					return err
				}
				p.GnssStatus = &value
			}
		case constants.Offset:
			offset, err := strconv.ParseFloat(matched[i], 64)
			if err != nil {
				return err
			}
			p.Offset = &offset
		case constants.ServoState:
			p.ServoState = matched[i]
		}
	}
	return nil
}

// NewGNSSExtractor creates a new metrics extractor for GNSS process
func NewGNSSExtractor() *BaseMetricsExtractor[*gnssParsed] {
	return &BaseMetricsExtractor[*gnssParsed]{
		ProcessNameStr: constants.GNSS,
		NewParsed:      func() *gnssParsed { return &gnssParsed{} },
		RegexExtractorPairs: []RegexExtractorPair[*gnssParsed]{
			{
				Regex: gnssRegex,
				Extractor: func(parsed *gnssParsed) (*Metrics, *PTPEvent, error) {
					metric, err := extractGNSSMetrics(parsed)
					return metric, nil, err
				},
			},
		},
	}
}

func extractGNSSMetrics(parsed *gnssParsed) (*Metrics, error) {
	if parsed.Interface == "" {
		return nil, errors.New("gnss interface is empty")
	}

	if parsed.Offset == nil {
		return nil, errors.New("gnss offset is empty")
	}

	clockState := clockStateFromServo(parsed.ServoState)

	var statusMetrics []StatusMetric
	if parsed.GnssStatus != nil {
		statusMetrics = append(statusMetrics, StatusMetric{
			Subtype: "gnss_status",
			Status:  *parsed.GnssStatus,
		})
	}

	metrics := &Metrics{
		From:       constants.GNSS,
		Iface:      parsed.Interface,
		Offset:     *parsed.Offset,
		MaxOffset:  *parsed.Offset,
		ClockState: clockState,
		Source:     constants.GNSS,
		Status:     statusMetrics,
	}

	return metrics, nil
}
