package clock

import (
	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
)

// OffsetSample carries an aggregate offset and the interface label identifying
// its metric series. The label does not imply that the value came only from
// that interface.
type OffsetSample struct {
	IFace  string
	Offset int64
}

// Clock represents a PTP clock instance tied to a specific config profile.
type Clock interface {
	AddEvent(ev event.Event) EventResult
	GetState() event.PTPState
	SystemClockUpdate()
	Reset()
	ConfigName() string
	// ConfigNames returns every config name events for this clock may arrive
	// under, so the clock manager can route them all here. It always includes
	// ConfigName.
	ConfigNames() []string
	ClockType() event.ClockType
	ClockClass() fbprotocol.ClockClass
	// ClockOffsetMetric returns the clock's aggregate offset when available.
	ClockOffsetMetric() (OffsetSample, bool)
	SetIPC(func(message ipc.Message))
	SetEventLoopbackFunc(f func(event.Event))
	GetData(processName event.EventSource) *event.Data
	ProcessData() []*event.Data
	SetOsClock(*OsClock)
}

// ManagedWorkerOwner is an optional capability for clocks with explicitly managed
// background workers. StopManagedWorkers terminally cancels those workers without
// joining them and returns a non-nil channel closed when they finish. It is
// idempotent, including before any worker starts, and prevents worker restart.
// Callers serialize it with other clock operations and join outside the event
// lock. Reset remains nonterminal. Completion excludes untracked legacy work;
// clocks without this capability make no managed-worker completion guarantee.
type ManagedWorkerOwner interface {
	StopManagedWorkers() <-chan struct{}
}

// EventResult combines synchronization state with the clock's reporting decision.
// A nil Report preserves the input event; a non-nil Report replaces it for logs
// and event metrics. SuppressReport takes precedence and suppresses reporting
// and the manager's subsequent state notifications, metrics and statistics.
// Reporting decisions leave the raw event and stored policy inputs intact.
type EventResult struct {
	SyncState
	Report         *event.Event
	SuppressReport bool
}

// SyncState holds the composite synchronization state of a clock.
type SyncState struct {
	State          event.PTPState
	ClockClass     fbprotocol.ClockClass
	SourceLost     bool
	ClkLog         string
	LastLoggedTime int64
	LeadingIFace   string
	ClockAccuracy  fbprotocol.ClockAccuracy
	ClockOffset    int64
	// HAProfileStatus maps each HA member profile name to whether it is the
	// currently selected (active) source. Exactly one member is active at a time
	// (phc2sys disciplines the system clock from a single source). It is nil for
	// every non-HA clock
	HAProfileStatus map[string]bool
}

func emitOverallSyncStateIfChanged(sendIPC func(ipc.Message), overallSyncState *event.PTPState, clockState, osClockState event.PTPState, profile string) {
	next := event.WorstOfState(clockState, osClockState)
	if next != *overallSyncState {
		*overallSyncState = next
		sendIPC(ipc.Message{
			Type:    ipc.TypeSyncState,
			Profile: profile,
			Values:  ipc.SyncStateValue{State: event.PtpStateToIPCState(next)},
		})
	}
}
