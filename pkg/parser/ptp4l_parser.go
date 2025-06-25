package parser

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
)

var (
	ptp4lEventRegex = regexp.MustCompile(
		`^ptp4l\[(?P<timestamp>\d+\.?\d*)\]:` +
			`\s+\[(?P<config_name>.*\.\d+\.config):?(?P<serverity>\d*)\]` +
			`\s+port\s+(?P<port>\d+):` +
			`\s+(?P<event>.+)`,
	)
	// ptp4l rms regex
	summaryPTP4LRegex = regexp.MustCompile(
		`^ptp4l\[(?P<timestamp>\d+\.?\d*)\]:` +
			`\s+\[(?P<config_name>.*\.\d+\.config):?(?P<serverity>\d*)\]` +
			`\s*(?P<interface>\w+)?` +
			`\s+rms\s+(?P<offset>-?\d+)` +
			`\s+max\s+(?P<max>-?\d+)` +
			`\s+freq\s+(?P<freq_adj>[-+]\d+)\s+\+/-\s+\d+` +
			`\s*(?:delay\s+(?P<delay>\d+)\s+\+/-\s+\d+)?` +
			`$`,
	)
	// ptp4l master offset regex
	regularPTP4LRegex = regexp.MustCompile(
		`^ptp4l\[(?P<timestamp>\d+\.?\d*)\]:` +
			`\s+\[(?P<config_name>.*\.\d+\.config):?(?P<serverity>\d*)\]` +
			`\s*(?P<interface>\w+)?` +
			`\s+offset\s+(?P<offset>-?\d+)` +
			`\s+(?P<servo_state>s\d)` +
			`\s+freq\s+(?P<freq_adj>[-+]\d+)` +
			`\s*(?:path\s+delay\s+(?P<delay>\d+))?` +
			`$`,
	)

	// ptp4l clock_class_change regex
	clockClassChangeRegex = regexp.MustCompile(
		`^ptp4l\[(?P<timestamp>\d+\.?\d*)\]:` +
			`\s+\[(?P<config_name>.*\.\d+\.config):?(?P<serverity>\d*)\]` +
			`\s+CLOCK_CLASS_CHANGE\s+(?P<clock_class>\d+)`,
	)
)

type ptp4lParsed struct {
	// Common
	Raw            string
	Timestamp      string
	ConfigName     string
	ServerityLevel *int

	// Metric
	Interface  string
	Offset     *float64
	MaxOffset  *float64
	FreqAdj    *float64
	Delay      *float64
	ServoState string

	// Event Fields
	PortID     *int
	Event      string
	ClockClass *int
}

// Populate ...
func (p *ptp4lParsed) Populate(line string, matched, feilds []string) error {
	p.Raw = line
	for i, field := range feilds {
		switch field {
		case constants.Timestamp:
			p.Timestamp = matched[i]
		case constants.ConfigName:
			p.ConfigName = matched[i]
		case "serverity":
			if matched[i] == "" { // serverity is optional
				continue
			}
			serverityLevel, err := strconv.Atoi(matched[i])
			if err != nil {
				return err
			}
			p.ServerityLevel = &serverityLevel
		case constants.Interface:
			p.Interface = matched[i]
		case constants.Offset:
			if matched[i] == "" {
				return errors.New("offset cannot be empty")
			}
			offset, err := strconv.ParseFloat(matched[i], 64)
			if err != nil {
				return err
			}
			p.Offset = &offset
		case "max":
			if matched[i] == "" {
				return errors.New("max cannot be empty")
			}
			maxOffset, err := strconv.ParseFloat(matched[i], 64)
			if err != nil {
				return err
			}
			p.MaxOffset = &maxOffset
		case "freq_adj":
			if matched[i] == "" {
				return errors.New("freq_adj cannot be empty")
			}
			freqAdj, err := strconv.ParseFloat(matched[i], 64)
			if err != nil {
				return err
			}
			p.FreqAdj = &freqAdj
		case "delay":
			if matched[i] == "" { // Delay is optional
				continue
			}
			delay, err := strconv.ParseFloat(matched[i], 64)
			if err != nil {
				return err
			}
			p.Delay = &delay
		case constants.ServoState:
			p.ServoState = matched[i]
		case "port":
			port, err := strconv.Atoi(matched[i])
			if err != nil {
				return err
			}
			p.PortID = &port
		case "event":
			p.Event = matched[i]
		case "clock_class":
			clockClass, err := strconv.Atoi(matched[i])
			if err != nil {
				return err
			}
			p.ClockClass = &clockClass
		}
	}
	return nil
}

// NewPTP4LExtractor creates a new PTP4LExtractor.
func NewPTP4LExtractor() *BaseMetricsExtractor[*ptp4lParsed] {
	return &BaseMetricsExtractor[*ptp4lParsed]{
		ProcessNameStr: constants.PTP4L,
		NewParsed:      func() *ptp4lParsed { return &ptp4lParsed{} },
		RegexExtractorPairs: []RegexExtractorPair[*ptp4lParsed]{
			{
				Regex: ptp4lEventRegex,
				Extractor: func(parsed *ptp4lParsed) (*Metrics, *PTPEvent, error) {
					event, err := extractEventPTP4l(parsed)
					return nil, event, err
				},
			},
			{
				Regex: summaryPTP4LRegex,
				Extractor: func(parsed *ptp4lParsed) (*Metrics, *PTPEvent, error) {
					metric, err := extractSummaryPTP4l(parsed)
					return metric, nil, err
				},
			},
			{
				Regex: regularPTP4LRegex,
				Extractor: func(parsed *ptp4lParsed) (*Metrics, *PTPEvent, error) {
					metric, err := extractRegularPTP4l(parsed)
					return metric, nil, err
				},
			},
			{
				Regex: clockClassChangeRegex,
				Extractor: func(parsed *ptp4lParsed) (*Metrics, *PTPEvent, error) {
					event, err := extractClockClassChangePTP4l(parsed)
					return nil, event, err
				},
			},
		},
	}
}
func extractEventPTP4l(parsed *ptp4lParsed) (*PTPEvent, error) {
	if parsed.PortID == nil {
		return nil, fmt.Errorf("port id not found")
	}
	portID := *parsed.PortID

	role, err := determineRole(parsed.Event)
	if err != nil {
		portID = 0
	}

	return &PTPEvent{
		PortID: portID,
		Role:   role,
		Raw:    parsed.Raw,
	}, err
}

func extractClockClassChangePTP4l(parsed *ptp4lParsed) (*PTPEvent, error) {
	if parsed.ClockClass == nil {
		return nil, fmt.Errorf("clock class not found")
	}

	return &PTPEvent{
		PortID:     0, // Clock class changes are not port-specific
		Role:       constants.PortRoleUnknown,
		ClockClass: *parsed.ClockClass,
		Raw:        parsed.Raw,
	}, nil
}

func extractSummaryPTP4l(parsed *ptp4lParsed) (*Metrics, error) {
	iface := parsed.Interface
	if iface == "" {
		iface = constants.Master
	}
	if parsed.Offset == nil {
		return nil, errors.New("failed to find offset")
	}

	if parsed.MaxOffset == nil {
		return nil, errors.New("failed to find max offset")
	}

	if parsed.FreqAdj == nil {
		return nil, errors.New("failed to find freq adj")
	}

	var delay float64
	if parsed.Delay == nil {
		glog.Warning("delay is missing")
	} else {
		delay = *parsed.Delay
	}

	return &Metrics{
		Iface:     iface,
		Offset:    *parsed.Offset,
		MaxOffset: *parsed.MaxOffset,
		FreqAdj:   *parsed.FreqAdj,
		Delay:     delay,
		Source:    constants.Master,
	}, nil
}

func extractRegularPTP4l(parsed *ptp4lParsed) (*Metrics, error) {
	if parsed.Offset == nil {
		return nil, errors.New("failed to find offset")
	}

	if parsed.FreqAdj == nil {
		return nil, errors.New("failed to find freq adj")
	}

	var delay float64
	if parsed.Delay == nil {
		glog.Warning("delay is missing")
	} else {
		delay = *parsed.Delay
	}

	if parsed.ServoState == "" {
		return nil, errors.New("failed to find clock state")
	}
	clockState := clockStateFromServo(parsed.ServoState)

	return &Metrics{
		Iface:      parsed.Interface,
		Offset:     *parsed.Offset,
		MaxOffset:  *parsed.Offset,
		FreqAdj:    *parsed.FreqAdj,
		Delay:      delay,
		ClockState: clockState,
		Source:     constants.Master,
	}, nil
}

func determineRole(event string) (constants.PTPPortRole, error) {
	switch {
	case strings.Contains(event, "UNCALIBRATED to SLAVE"):
		return constants.PortRoleSlave, nil
	case strings.Contains(event, "UNCALIBRATED to PASSIVE"), strings.Contains(event, "MASTER to PASSIVE"), strings.Contains(event, "SLAVE to PASSIVE"):
		return constants.PortRolePassive, nil
	case strings.Contains(event, "UNCALIBRATED to MASTER"), strings.Contains(event, "LISTENING to MASTER"):
		return constants.PortRoleMaster, nil
	case strings.Contains(event, "FAULT_DETECTED"), strings.Contains(event, "SYNCHRONIZATION_FAULT"):
		return constants.PortRoleFaulty, nil
	case strings.Contains(event, "UNCALIBRATED to LISTENING"), strings.Contains(event, "SLAVE to LISTENING"), strings.Contains(event, "INITIALIZING to LISTENING"):
		return constants.PortRoleListening, nil
	default:
		return constants.PortRoleUnknown, fmt.Errorf("unrecognized role in event: %s", event)
	}
}
