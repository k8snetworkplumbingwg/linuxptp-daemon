package parser

import (
	"errors"
	"regexp"
	"strconv"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
)

// dpll[1700598434]:[ts2phc.0.config] ens2f0 frequency_status 3 offset 0 phase_status 3 pps_status 1 s2
var dpllRegex = regexp.MustCompile(
	`^dpll\[(?P<timestamp>\d+\.?\d*)\]:` +
		`\s*\[(?P<config_name>.*\.\d+\.c.*g)\]` +
		`\s+(?P<interface>\w+)` +
		`\s+frequency_status\s+(?P<frequency_status>[0-9]+)` +
		`\s+offset\s+(?P<offset>-?\d+)` +
		`\s+phase_status\s+(?P<phase_status>[0-9]+)` +
		`\s+pps_status\s+(?P<pps_status>[0-9]+)` +
		`\s+(?P<servo_state>s\d+)$`,
)

type dpllParsed struct {
	Raw             string
	Timestamp       string
	ConfigName      string
	SeverityLevel   *int
	Interface       string
	FrequencyStatus *float64
	Offset          *float64
	PhaseStatus     *float64
	PpsStatus       *float64
	ServoState      string
}

// Populate populates the dpllParsed struct from regex matches
func (p *dpllParsed) Populate(line string, matched, fields []string) error {
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
		case "frequency_status":
			if matched[i] != "" {
				value, err := strconv.ParseFloat(matched[i], 64)
				if err != nil {
					return err
				}
				p.FrequencyStatus = &value
			}
		case constants.Offset:
			offset, err := strconv.ParseFloat(matched[i], 64)
			if err != nil {
				return err
			}
			p.Offset = &offset
		case "phase_status":
			if matched[i] != "" {
				value, err := strconv.ParseFloat(matched[i], 64)
				if err != nil {
					return err
				}
				p.PhaseStatus = &value
			}
		case "pps_status":
			if matched[i] != "" {
				value, err := strconv.ParseFloat(matched[i], 64)
				if err != nil {
					return err
				}
				p.PpsStatus = &value
			}
		case constants.ServoState:
			p.ServoState = matched[i]
		}
	}
	return nil
}

// NewDPLLExtractor creates a new DPLL metrics extractor
func NewDPLLExtractor() *BaseMetricsExtractor[*dpllParsed] {
	return &BaseMetricsExtractor[*dpllParsed]{
		ProcessNameStr: constants.DPLL,
		NewParsed:      func() *dpllParsed { return &dpllParsed{} },
		RegexExtractorPairs: []RegexExtractorPair[*dpllParsed]{
			{
				Regex: dpllRegex,
				Extractor: func(parsed *dpllParsed) (*Metrics, *PTPEvent, error) {
					metric, err := extractDPLLMetrics(parsed)
					return metric, nil, err
				},
			},
		},
	}
}

func extractDPLLMetrics(parsed *dpllParsed) (*Metrics, error) {
	if parsed.Interface == "" {
		return nil, errors.New("dpll interface is empty")
	}

	if parsed.Offset == nil {
		return nil, errors.New("dpll offset is empty")
	}

	clockState := clockStateFromServo(parsed.ServoState)

	// Create status metrics list
	var statusMetrics []StatusMetric

	// Add frequency status if available
	if parsed.FrequencyStatus != nil {
		statusMetrics = append(statusMetrics, StatusMetric{
			Subtype: "frequency_status",
			Status:  *parsed.FrequencyStatus,
		})
	}

	// Add phase status if available
	if parsed.PhaseStatus != nil {
		statusMetrics = append(statusMetrics, StatusMetric{
			Subtype: "phase_status",
			Status:  *parsed.PhaseStatus,
		})
	}

	// Add PPS status if available
	if parsed.PpsStatus != nil {
		statusMetrics = append(statusMetrics, StatusMetric{
			Subtype: "pps_status",
			Status:  *parsed.PpsStatus,
		})
	}

	// Create base metrics
	metrics := &Metrics{
		From:       constants.DPLL,
		Iface:      parsed.Interface,
		Offset:     *parsed.Offset,
		MaxOffset:  *parsed.Offset,
		ClockState: clockState,
		Source:     constants.DPLL,
		Status:     statusMetrics,
	}

	return metrics, nil
}
