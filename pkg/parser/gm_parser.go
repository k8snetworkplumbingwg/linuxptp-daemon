package parser

import (
	"errors"
	"regexp"
	"strconv"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
)

// GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s0
var gmRegex = regexp.MustCompile(
	`^GM\[(?P<timestamp>\d+\.?\d*)\]:` +
		`\s*\[(?P<config_name>.*\.\d+\.c.*g)\]` +
		`\s+(?P<interface>\w+)` +
		`\s+T-GM-STATUS\s+(?P<gm_status>s\d+)$`,
)

type gmParsed struct {
	Raw           string
	Timestamp     string
	ConfigName    string
	SeverityLevel *int
	Interface     string
	GmStatus      string
}

// Populate populates the gmParsed struct from regex matches
func (p *gmParsed) Populate(line string, matched, fields []string) error {
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
		case "gm_status":
			p.GmStatus = matched[i]
		}
	}
	return nil
}

// NewGMExtractor creates a new metrics extractor for GM process
func NewGMExtractor() *BaseMetricsExtractor[*gmParsed] {
	return &BaseMetricsExtractor[*gmParsed]{
		ProcessNameStr: constants.GM,
		NewParsed:      func() *gmParsed { return &gmParsed{} },
		RegexExtractorPairs: []RegexExtractorPair[*gmParsed]{
			{
				Regex: gmRegex,
				Extractor: func(parsed *gmParsed) (*Metrics, *PTPEvent, error) {
					metric, err := extractGMMetrics(parsed)
					return metric, nil, err
				},
			},
		},
	}
}

func extractGMMetrics(parsed *gmParsed) (*Metrics, error) {
	if parsed.Interface == "" {
		return nil, errors.New("gm interface is empty")
	}

	clockState := clockStateFromServo(parsed.GmStatus)

	metrics := &Metrics{
		From:       constants.GM,
		Iface:      parsed.Interface,
		ClockState: clockState,
		Source:     constants.GM,
	}

	return metrics, nil
}
