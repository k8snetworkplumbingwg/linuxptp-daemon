package event

import (
	"fmt"
)

// TS2PHCRole is the synchronization role reported by ts2phc for a PHC.
// The empty value means that an observation does not describe a role change.
type TS2PHCRole string

const (
	// TS2PHCSink identifies a PHC selected as a synchronization sink.
	TS2PHCSink TS2PHCRole = "sink"
	// TS2PHCSource identifies the synchronization source PHC.
	TS2PHCSource TS2PHCRole = "source"
	// TS2PHCUnmanaged identifies a PHC with no known synchronization role.
	TS2PHCUnmanaged TS2PHCRole = "unmanaged"
)

// PHCTimestamp is a hardware timestamp split into whole seconds and nanoseconds.
type PHCTimestamp struct {
	Seconds     int64 `json:"seconds"`
	Nanoseconds int64 `json:"nanoseconds"`
}

// TS2PHCInterface identifies a configured interface and its physical clock.
type TS2PHCInterface struct {
	Name      string
	PHCDevice string
	Source    EventSource
}

// TS2PHCConfig is a resolved setup-time snapshot of identities and thresholds,
// not a clock-specific monitoring policy. It is supplied before process startup.
type TS2PHCConfig struct {
	Interfaces       []TS2PHCInterface
	LeadingInterface string
	Threshold        PtpClockThreshold
}

func (TS2PHCConfig) clockConfiguration() {}

// TS2PHCObservation carries process facts, before clock-specific evaluation.
// State is the state reported on an offset line, without threshold evaluation.
// PollSuccessful distinguishes real servo offsets from NMEA status metrics.
type TS2PHCObservation struct {
	Timestamp      string
	PHCDevice      string
	PHCTstamp      *PHCTimestamp
	Role           TS2PHCRole
	PollTimeout    bool
	PollSuccessful bool
	Holdover       bool
	Offset         *float64
	State          PTPState
	NMEALocked     *bool
}

// TS2PHCRunStarted resets per-run observations without changing configuration
// or the clock's independently tracked upstream/DPLL state.
type TS2PHCRunStarted struct{}

func (*TS2PHCRunStarted) eventData() {}

func (*TS2PHCRunStarted) clockInput() {}

func (*TS2PHCRunStarted) String() string { return "ts2phc run started" }

func (*TS2PHCObservation) eventData() {}

func (*TS2PHCObservation) clockInput() {}

func (d *TS2PHCObservation) String() string {
	return fmt.Sprintf("ts2phc observation: clock=%s timestamp=%s role=%s timeout=%t", d.PHCDevice, d.Timestamp, d.Role, d.PollTimeout)
}
