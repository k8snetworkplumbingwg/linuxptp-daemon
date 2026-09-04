package process

import (
	"context"
	"fmt"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

// type process interface {
// 	Name() string
// 	Stopped() bool
// 	CmdStop()
// 	CmdInit()
// 	ProcessStatus(c net.Conn, status int64)
// 	CmdRun(stdToSocket bool)
// 	ExitCh() chan struct{}
// }

// Action is an enumeration for process control operations.
type Action int

// Action enumeration for process control operations.
const (
	ActionStart   Action = iota // launch the process
	ActionStop                  // gracefully stop the process
	ActionRestart               // stop then start
	ActionEnable                // logical enable (process stays running)
	ActionDisable               // logical disable (process stays running)
)

// State is an enumeration for lifecycle stages of a process.
type State int

// process.State enumeration for lifecycle stages of a process.
const (
	Created State = iota
	Starting
	Running
	Stopping
	Stopped
	Dead
)

// String returns the string representation of the State.
func (s State) String() string {
	switch s {
	case Created:
		return "Created"
	case Starting:
		return "Starting"
	case Running:
		return "Running"
	case Stopping:
		return "Stopping"
	case Stopped:
		return "Stopped"
	case Dead:
		return "Dead"
	default:
		return fmt.Sprintf("ProcessState(%d)", int(s))
	}
}

// Enabler is an optional interface for processes that support logical
// enable/disable without stopping. Only chronydProcess implements this.
type Enabler interface {
	Enable() error
	Disable() error
	IsEnabled() bool
}

// Process represents a manageable PTP process with lifecycle control and event-driven conditions.
type Process interface {
	Name() string
	Start(ctx context.Context) error
	Stop() error
	Conditions() map[Action]Condition
	State() State
	Profile() *ptpv1.PtpProfile    // Should go away
	ClockType() event.ClockType    // Should go away
	DependentProcesses() []Process // Should go away
}
