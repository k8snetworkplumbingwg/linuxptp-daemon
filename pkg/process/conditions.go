package process

import (
	"fmt"
	"math"
	"strings"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
)

// GnssRecoveredEventName and GnssFailoverEventName are GNSS event names for monitoring state changes.
const (
	GnssRecoveredEventName = "gnss_recovered"
	GnssFailoverEventName  = "gnss_failover"
)

// EventStats holds aggregated offset statistics from PTP events.
type EventStats struct {
	data map[string]*utils.Window
}

// NewEventStats creates an EventStats from a map of offset windows keyed by config name.
func NewEventStats(data map[string]*utils.Window) EventStats {
	return EventStats{data: data}
}

// Window returns the offset statistics window for the given config name, or nil if not found.
func (s EventStats) Window(cfgName string) *utils.Window {
	if s.data == nil {
		return nil
	}
	return s.data[cfgName]
}

// Condition is a predicate that determines whether an action should be taken on a process.
type Condition interface {
	// Met returns true when this condition is satisfied by the event.
	// p is the process that owns this condition.
	Met(p Process, ev event.Event, stats EventStats) bool
	// String returns a human-readable description for logging.
	String() string
}

// GetCondition returns the condition for action. If no condition is
// configured, fallback is returned. Missing Restart reuses Start's condition.
func GetCondition(p Process, a Action, fallback Condition) Condition {
	if p == nil {
		return fallback
	}
	conds := p.Conditions()
	if c := lookup(conds, a); c != nil {
		return c
	}
	if a == ActionRestart {
		if c := lookup(conds, ActionStart); c != nil {
			return c
		}
	}
	return fallback
}

// Never is a condition that is never met.
type Never struct{}

// Met implements the Condition interface for Never.
// It always returns false since Never conditions are never satisfied.
func (c Never) Met(_ Process, _ event.Event, _ EventStats) bool { return false }
func (c Never) String() string                                  { return "Never" }

func lookup(conds map[Action]Condition, a Action) Condition {
	if conds == nil {
		return nil
	}
	c, ok := conds[a]
	if !ok || c == nil {
		return nil
	}
	return c
}

func eventEmpty(ev event.Event) bool {
	return ev.Source == "" && ev.CfgName == "" && ev.Data == nil && !ev.Reset
}

func condLog(p Process, c Condition, met bool, detail string) {
	name := ""
	if p != nil {
		name = p.Name()
	}
	glog.Infof("condition %s process=%s met=%v %s", c, name, met, detail)
}

// Immediate is a condition that is always met immediately.
type Immediate struct {
	ConfigName string
}

// Met implements the Condition interface for Immediate.
// It always returns true since Immediate conditions are always satisfied.
func (c Immediate) Met(_ Process, _ event.Event, _ EventStats) bool {
	return true
}

func (c Immediate) String() string {
	return "Immediate"
}

// OnStateAndOffsetForCount is a condition that checks PTP state and offset magnitude within a rolling window.
type OnStateAndOffsetForCount struct {
	ConfigName string
	Source     string
	State      event.PTPState
	MaxOffset  float64
	Count      int
}

// Met implements the Condition interface for OnStateAndOffsetForCount.
// It returns true when the event's config and state match, and enough recent samples
// in the offset window show magnitude below MaxOffset.
func (c OnStateAndOffsetForCount) Met(p Process, ev event.Event, stats EventStats) bool {
	if eventEmpty(ev) {
		return false
	}
	related := ev.CfgName == c.ConfigName || (c.Source != "" && c.Source == string(ev.Source))
	if c.ConfigName == "" || c.ConfigName != ev.CfgName {
		if related {
			condLog(p, c, false, fmt.Sprintf("cfg want=%s got=%s", c.ConfigName, ev.CfgName))
		}
		return false
	}
	if c.Source != "" && c.Source != string(ev.Source) {
		condLog(p, c, false, fmt.Sprintf("source want=%s got=%s", c.Source, ev.Source))
		return false
	}
	data, ok := eventPTPState(ev.Data)
	if !ok {
		condLog(p, c, false, "event has no PTP state")
		return false
	}
	if c.State != data {
		condLog(p, c, false, fmt.Sprintf("state want=%s got=%s", c.State, data))
		return false
	}
	w := stats.Window(ev.CfgName)
	if w == nil {
		condLog(p, c, false, "no offset window")
		return false
	}
	nSamples := w.CountSamples(func(x float64) bool {
		return math.Abs(x) < c.MaxOffset
	})
	met := nSamples > c.Count
	condLog(p, c, met, fmt.Sprintf("window_samples=%d need>%d", nSamples, c.Count))
	return met
}

func (c OnStateAndOffsetForCount) String() string {
	return fmt.Sprintf("OnStateAndOffsetForCount source=%s cfg=%s state=%v maxOffset=%v count=%d", c.Source, c.ConfigName, c.State, c.MaxOffset, c.Count)
}

// OnProcessState is a condition that checks for a specific PTP synchronization state.
type OnProcessState struct {
	ConfigName string
	Source     string
	State      event.PTPState
}

// Met implements the Condition interface for OnProcessState.
// It returns true when the event carries a PTP state payload with the expected state.
func (c OnProcessState) Met(p Process, ev event.Event, _ EventStats) bool {
	if eventEmpty(ev) {
		return false
	}
	if c.ConfigName != ev.CfgName {
		if c.Source == string(ev.Source) {
			condLog(p, c, false, fmt.Sprintf("cfg want=%s got=%s", c.ConfigName, ev.CfgName))
		}
		return false
	}
	data, ok := eventPTPState(ev.Data)
	if !ok {
		condLog(p, c, false, "event has no PTP state")
		return false
	}
	met := c.State == data
	condLog(p, c, met, fmt.Sprintf("state want=%s got=%s", c.State, data))
	return met
}

func (c OnProcessState) String() string {
	return fmt.Sprintf("OnProcessState source=%s cfg=%s state=%v", c.Source, c.ConfigName, c.State)
}

// OnProcessUp is true when the event is PROCESS_STATUS Up for Source / CfgName.
type OnProcessUp struct {
	ConfigName string
	Source     event.EventSource
}

// Met implements the Condition interface for OnProcessUp.
// It returns true when the event indicates the specified process is up (status = 1).
func (c OnProcessUp) Met(p Process, ev event.Event, _ EventStats) bool {
	if eventEmpty(ev) {
		return false
	}
	if c.Source != "" && ev.Source != c.Source {
		return false
	}
	if c.ConfigName != "" && ev.CfgName != c.ConfigName {
		condLog(p, c, false, fmt.Sprintf("cfg want=%s got=%s", c.ConfigName, ev.CfgName))
		return false
	}
	ps, ok := ev.Data.(*event.ProcessStatusData)
	var status int64
	if ok {
		status = ps.Status
	}
	met := ok && status == 1
	condLog(p, c, met, fmt.Sprintf("process_status=%v ok=%v", status, ok))
	return met
}

// String returns the string representation of OnProcessUp.
func (c OnProcessUp) String() string {
	return fmt.Sprintf("OnProcessUp source=%s cfg=%s", c.Source, c.ConfigName)
}

// All is true when every nested condition is true for the same event.
type All struct {
	Conditions []Condition
}

// Met implements the Condition interface for All.
// It returns true only when all nested conditions are met by the same event.
func (c All) Met(p Process, ev event.Event, stats EventStats) bool {
	if len(c.Conditions) == 0 {
		return true
	}
	for _, cond := range c.Conditions {
		if cond == nil || !cond.Met(p, ev, stats) {
			return false
		}
	}
	return true
}

// String returns the string representation of All.
func (c All) String() string {
	parts := make([]string, 0, len(c.Conditions))
	for _, cond := range c.Conditions {
		if cond == nil {
			parts = append(parts, "<nil>")
			continue
		}
		parts = append(parts, cond.String())
	}
	return "All(" + strings.Join(parts, ", ") + ")"
}

// Any is true when at least one nested condition is true for the event.
type Any struct {
	Conditions []Condition
}

// Met implements the Condition interface for Any.
// It returns true when at least one nested condition is met by the event.
func (c Any) Met(p Process, ev event.Event, stats EventStats) bool {
	for _, cond := range c.Conditions {
		if cond != nil && cond.Met(p, ev, stats) {
			return true
		}
	}
	return false
}

// String returns the string representation of Any.
func (c Any) String() string {
	parts := make([]string, 0, len(c.Conditions))
	for _, cond := range c.Conditions {
		if cond == nil {
			parts = append(parts, "<nil>")
			continue
		}
		parts = append(parts, cond.String())
	}
	return "Any(" + strings.Join(parts, ", ") + ")"
}

// OnPluginEvent is true when the event carries PluginData with a matching EventName.
type OnPluginEvent struct {
	EventName string
}

// Met implements the Condition interface for OnPluginEvent.
// It returns true when the event carries PluginData with the matching EventName.
func (c OnPluginEvent) Met(p Process, ev event.Event, _ EventStats) bool {
	if eventEmpty(ev) {
		return false
	}
	pd, ok := ev.Data.(*event.PluginData)
	if !ok {
		return false
	}
	met := pd.EventName == c.EventName
	condLog(p, c, met, fmt.Sprintf("event=%s", pd.EventName))
	return met
}

// String returns the string representation of OnPluginEvent.
func (c OnPluginEvent) String() string {
	return fmt.Sprintf("OnPluginEvent event=%s", c.EventName)
}

// OnProcessDown is true when the event is PROCESS_STATUS Down for Source / CfgName.
type OnProcessDown struct {
	ConfigName string
	Source     event.EventSource
}

// Met implements the Condition interface for OnProcessDown.
// It returns true when the event indicates the specified process is down (status = 0).
func (c OnProcessDown) Met(p Process, ev event.Event, _ EventStats) bool {
	if eventEmpty(ev) {
		return false
	}
	if c.Source != "" && ev.Source != c.Source {
		return false
	}
	if c.ConfigName != "" && ev.CfgName != c.ConfigName {
		condLog(p, c, false, fmt.Sprintf("cfg want=%s got=%s", c.ConfigName, ev.CfgName))
		return false
	}
	ps, ok := ev.Data.(*event.ProcessStatusData)
	var status int64
	if ok {
		status = ps.Status
	}
	met := ok && status == 0
	condLog(p, c, met, fmt.Sprintf("process_status=%v ok=%v", status, ok))
	return met
}

// String returns the string representation of OnProcessDown.
func (c OnProcessDown) String() string {
	return fmt.Sprintf("OnProcessDown source=%s cfg=%s", c.Source, c.ConfigName)
}

func eventPTPState(data event.EventData) (event.PTPState, bool) {
	switch d := data.(type) {
	case *event.OffsetData:
		return d.State, true
	case *event.DPLLData:
		return d.State, true
	case *event.StateData:
		return d.State, true
	case *event.SyncEData:
		return d.State, true
	}
	return "", false
}
