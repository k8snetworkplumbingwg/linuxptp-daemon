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

// EventReport is an event accepted by a clock for logging and metric export.
// OffsetSource overrides the offset metric's source label when nonempty.
// OffsetUnavailable invalidates the current offset without inventing a value.
type EventReport struct {
	Event             event.Event
	OffsetSource      string
	OffsetUnavailable bool
	// MetricValues carries auxiliary values when the event payload alone does
	// not represent every metric produced by the clock's decision.
	MetricValues map[event.ValueType]interface{}
}

// OffsetMetrics describes the offset series a source currently expects. When
// Invalidate is true, retained series are marked unavailable for a new run.
type OffsetMetrics struct {
	From       string
	Interfaces []string
	Invalidate bool
}

// SourceState is a source's current state on one interface.
type SourceState struct {
	IFace string
	State event.PTPState
}

// StateMetrics describes the current per-interface states for a source.
type StateMetrics struct {
	Details []SourceState
}

// SourceMetrics describes source-owned metric series independently of how they
// are exported. Nil Offsets or States leave that group unchanged. ClearOtherSources
// removes metrics from other sources under ConfigName, as part of a source reset.
type SourceMetrics struct {
	ConfigName        string
	Source            event.EventSource
	Offsets           *OffsetMetrics
	States            *StateMetrics
	ClearOtherSources bool
}

// EventResult carries the final clock state and reporting effects of one input.
// Events is authoritative: an empty list means no event should be reported.
// Ignored inputs have no state or reporting effects.
type EventResult struct {
	SyncState
	Events  []EventReport
	Metrics []SourceMetrics
	Ignored bool
}

func eventResult(state SyncState, ev event.Event) EventResult {
	return EventResult{SyncState: state, Events: []EventReport{{Event: ev}}}
}

// Configurable accepts setup-time configuration while clock mutation is serialized.
type Configurable interface {
	Configure(configName string, config event.ClockConfiguration) ([]SourceMetrics, error)
}

// ResetMetricProvider describes the source metrics retained after a reset.
type ResetMetricProvider interface {
	MetricsAfterReset(configName string, source event.EventSource) []SourceMetrics
}

// ApplyingEventPolicy decides which events remain valid during profile apply.
// An accepted event can request that aggregate state reporting be deferred.
type ApplyingEventPolicy interface {
	DuringApply(ev event.Event) (accept, reportState bool)
}

// Clock represents a PTP clock instance tied to a specific config profile.
type Clock interface {
	// AddEvent lets the clock interpret an input and return its reporting effects.
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
	// ClockClassMetricConfigName identifies the per-config clock-class series.
	ClockClassMetricConfigName(eventConfigName string) string
	// ClockOffsetMetric returns the clock's aggregate offset when available.
	ClockOffsetMetric() (OffsetSample, bool)
	SetIPC(func(message ipc.Message))
	SetEventLoopbackFunc(f func(event.Event))
	GetData(processName event.EventSource) *event.Data
	ProcessData() []*event.Data
	SetOsClock(*OsClock)
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
