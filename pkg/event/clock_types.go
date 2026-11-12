package event

// ClockType ...
type ClockType string

const (
	// TGM ...
	TGM ClockType = "T-GM"
	// BC ...
	BC ClockType = "BC"
	// TBC is a Telco Boundary Clock (T-BC) with DPLL, ts2phc, and E810 plugin.
	TBC ClockType = "T-BC"
	// OC ...
	OC ClockType = "OC"
	// HA is a High Availability clock: a composite of multiple BC instances with a
	// single phc2sys that fails over between them to discipline the system clock.
	HA ClockType = "HA"
	// SysClock only the system clock not a PTP Clock (this is updated by another clock)
	SysClock ClockType = "SysClock"
	// ClockUnset ...
	ClockUnset ClockType = ""
)
