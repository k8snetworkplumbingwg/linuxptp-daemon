package constants

// BMCASelection describes the reference selected by one ptp4l instance. The
// unknown value is also used for log lines that do not report a BMCA decision.
type BMCASelection string

const (
	// BMCAUnknown means no reference selection has been observed.
	BMCAUnknown BMCASelection = ""
	// BMCALocal means the instance selected its local clock.
	BMCALocal BMCASelection = "local"
	// BMCAExternal means the instance selected an external reference.
	BMCAExternal BMCASelection = "external"
)
