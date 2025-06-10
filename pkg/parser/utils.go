package parser

import (
	"regexp"
	"strings"
)

const (
	PTPNamespace       = "openshift"
	PTPSubsystem       = "ptp"
	GNSS               = "gnss"
	DPLL               = "dpll"
	ptp4lProcessName   = "ptp4l"
	phc2sysProcessName = "phc2sys"
	ts2phcProcessName  = "ts2phc"
	syncEProcessName   = "synce4l"
	clockRealTime      = "CLOCK_REALTIME"
	master             = "master"
	pmcSocketName      = "pmc"

	faultyOffset = 999999

	offset = "offset"
	rms    = "rms"

	// offset source
	phc = "phc"
	sys = "sys"
)

const (
	//LOCKED ...
	LOCKED string = "LOCKED"
	//FREERUN ...
	FREERUN = "FREERUN"
	// HOLDOVER
	HOLDOVER = "HOLDOVER"
)

const (
	PtpProcessDown int64 = 0
	PtpProcessUp   int64 = 1
)

type PTPPortRole int

const (
	PASSIVE PTPPortRole = iota
	SLAVE
	MASTER
	FAULTY
	UNKNOWN
	LISTENING
)

const (
	MessageTagSuffixSeperator = ":"
)

var (
	messageTagSuffixRegEx = regexp.MustCompile(`([a-zA-Z0-9]+\.[a-zA-Z0-9]+\.config):[a-zA-Z0-9]+(:[a-zA-Z0-9]+)?`)
	clockIDRegEx          = regexp.MustCompile(`\/dev\/ptp\d+`)
)

func normalizeLine(line string) string {
	replacer := strings.NewReplacer("[", " ", "]", " ", ":", " ", " phc ", " ", " sys ", " ")
	return replacer.Replace(line)
}

func parseClockState(s string) string {
	switch s {
	case "s2", "s3":
		return "LOCKED"
	case "s0", "s1":
		return "FREERUN"
	default:
		return "FREERUN"
	}
}

// to remove log severity suffixes from the log messages
func removeMessageSuffix(input string) (output string) {
	// container log output  "ptp4l[2464681.628]: [phc2sys.1.config:7] master offset -4 s2 freq -26835 path delay 525"
	// make sure non-supported version can handle suffix tags
	// clear {} from unparsed template
	//"ptp4l[2464681.628]: [phc2sys.1.config:{level}] master offset -4 s2 freq -26835 path delay 525"
	replacer := strings.NewReplacer("{", "", "}", "")
	output = replacer.Replace(input)
	// Replace matching parts in the input string
	output = messageTagSuffixRegEx.ReplaceAllString(output, "$1")
	return output
}
