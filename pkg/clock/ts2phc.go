package clock

import (
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	parserconstants "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
)

// ts2phcPolicy is selected by the owning clock, not inferred by the tracker.
type ts2phcPolicy struct {
	skipPHC      string
	pollTimeouts bool
}

type ts2phcMetricOptions struct {
	includeOffsets    bool
	invalidateOffsets bool
	includeStates     bool
}

func ts2phcConfig(config event.ClockConfiguration) (event.TS2PHCConfig, bool) {
	switch value := config.(type) {
	case event.TS2PHCConfig:
		return value, true
	case *event.TS2PHCConfig:
		if value != nil {
			return *value, true
		}
	}
	return event.TS2PHCConfig{}, false
}

// ts2phcUpdate is applied once to the same clock, including when it only
// carries inactive-PHC cleanup and has no reportable events.
type ts2phcUpdate struct {
	events       []event.Event
	inactivePHCs []string
	trigger      event.Event
}

// ts2phcTracker correlates process observations on the serialized clock loop.
// Roles and holdover markers are facts; GM/TBC choose their monitoring policy.
type ts2phcTracker struct {
	config           event.TS2PHCConfig
	roles            map[string]event.TS2PHCRole
	selections       map[string]event.TS2PHCRole
	currentPollCycle *pollCycle
	offsetHoldover   bool
	timeoutGroup     int
	skipPHC          string
}

// pollCycle contains the timestamps for one PPS edge. A batch can cross a log
// second; only a repeated PHC with a new timestamp or a longer gap closes it.
type pollCycle struct {
	group           int
	lastSampleGroup int
	tstamps         []tstamp
	counted         bool
}

type tstamp struct {
	seconds     int64
	nanoseconds int64
	device      string
	iface       string
	role        event.TS2PHCRole
	// A reported or intentionally skipped sample is never reopened by reselection.
	handled bool
}

func (t *ts2phcTracker) configure(config event.TS2PHCConfig) {
	t.config = config
	t.config.Interfaces = slices.Clone(config.Interfaces)
	t.reset()
}

func (t *ts2phcTracker) reset() {
	t.roles = make(map[string]event.TS2PHCRole)
	for _, iface := range t.config.Interfaces {
		if iface.PHCDevice != "" {
			t.roles[iface.PHCDevice] = event.TS2PHCSink
		}
	}
	t.selections = make(map[string]event.TS2PHCRole)
	t.currentPollCycle = nil
	t.offsetHoldover = false
	t.timeoutGroup = 0
	t.skipPHC = ""
}

// prepare records metadata before the clock chooses a policy for this observation.
func (t *ts2phcTracker) prepare(observation *event.TS2PHCObservation) bool {
	if role := observation.Role; role != "" {
		if role != event.TS2PHCSink && role != event.TS2PHCSource && role != event.TS2PHCUnmanaged {
			return false
		}
		if _, configured := t.roles[observation.PHCDevice]; !configured {
			return false
		}
		t.roles[observation.PHCDevice] = role
		t.selections[observation.PHCDevice] = role
		if cycle := t.currentPollCycle; cycle != nil {
			for i := range cycle.tstamps {
				ts := &cycle.tstamps[i]
				if ts.device == observation.PHCDevice {
					ts.role = role
					if role != event.TS2PHCSink {
						ts.handled = true
					}
				}
			}
		}
	}
	if observation.PollSuccessful {
		t.offsetHoldover = observation.Holdover
	}
	return true
}

func (t *ts2phcTracker) deviceForInterface(iface string) string {
	for _, configured := range t.config.Interfaces {
		if configured.Name == iface || configured.PHCDevice == iface {
			return configured.PHCDevice
		}
	}
	return iface
}

func (t *ts2phcTracker) interfaceForDevice(device string) event.TS2PHCInterface {
	for _, iface := range t.config.Interfaces {
		if iface.PHCDevice == device {
			return iface
		}
	}
	return event.TS2PHCInterface{}
}

// offsetInterfaces identifies the canonical interfaces for which the clock
// currently expects measurements. Unselected PHCs and aliases are excluded.
func (t *ts2phcTracker) offsetInterfaces(policy ts2phcPolicy) []string {
	var interfaces []string
	seen := make(map[string]bool)
	for _, iface := range t.config.Interfaces {
		device := iface.PHCDevice
		if iface.Name == "" || seen[device] || device == policy.skipPHC || t.roles[device] != event.TS2PHCSink {
			continue
		}
		seen[device] = true
		interfaces = append(interfaces, iface.Name)
	}
	return interfaces
}

// observe produces clock events and cleanup instructions without changing the
// clock's data or synchronization state. prepare must run before choosing policy.
func (t *ts2phcTracker) observe(ev event.Event, observation *event.TS2PHCObservation, policy ts2phcPolicy) ts2phcUpdate {
	update := ts2phcUpdate{trigger: ev}
	if policy.skipPHC != "" && policy.skipPHC != t.skipPHC {
		update.inactivePHCs = append(update.inactivePHCs, policy.skipPHC)
	}
	if observation.Role != "" && t.roles[observation.PHCDevice] != event.TS2PHCSink &&
		!slices.Contains(update.inactivePHCs, observation.PHCDevice) {
		update.inactivePHCs = append(update.inactivePHCs, observation.PHCDevice)
	}
	update.events = t.events(ev, observation, policy)
	return update
}

func (t *ts2phcTracker) sourceMetrics(holder *DataHolder, configName string, policy ts2phcPolicy, options ts2phcMetricOptions) SourceMetrics {
	metrics := SourceMetrics{ConfigName: configName, Source: event.TS2PHC}
	if options.includeOffsets {
		metrics.Offsets = &OffsetMetrics{From: parserconstants.Master, Interfaces: t.offsetInterfaces(policy), Invalidate: options.invalidateOffsets}
	}
	if options.includeStates {
		metrics.States = &StateMetrics{}
		holder.dataMu.RLock()
		defer holder.dataMu.RUnlock()
		for _, data := range holder.data {
			if data == nil || data.ProcessName != event.TS2PHC {
				continue
			}
			for _, detail := range data.Details {
				if detail != nil && detail.IFace != "" && (detail.State == event.PTP_LOCKED || detail.State == event.PTP_FREERUN || detail.State == event.PTP_HOLDOVER) {
					metrics.States.Details = append(metrics.States.Details, SourceState{IFace: detail.IFace, State: detail.State})
				}
			}
		}
	}
	return metrics
}

func ts2phcEventResult(state SyncState, events []event.Event, metrics SourceMetrics) EventResult {
	result := EventResult{SyncState: state, Metrics: []SourceMetrics{metrics}}
	for _, ev := range events {
		_, unavailable := ev.Data.(*event.StateData)
		report := EventReport{Event: ev, OffsetSource: parserconstants.Master, OffsetUnavailable: unavailable}
		if _, stateOnly := ev.Data.(*event.StateData); stateOnly && ev.WriteToLog && state.State != event.PTP_LOCKED {
			// A GNSS sink with no timestamp has no offset value, but its NMEA
			// availability must not remain stale-LOCKED while the clock is FREERUN.
			report.MetricValues = map[event.ValueType]interface{}{event.NMEA_STATUS: false}
		}
		result.Events = append(result.Events, report)
	}
	return result
}

// discardInactiveTS2PHC removes stale fault state when a PHC stops being
// monitored, without inventing a successful offset or replacement LOCKED sample.
func (holder *DataHolder) discardInactiveTS2PHC(t *ts2phcTracker, inactivePHCs []string) bool {
	if len(inactivePHCs) == 0 {
		return false
	}
	holder.dataMu.Lock()
	defer holder.dataMu.Unlock()
	changed := false
	for _, data := range holder.data {
		if data.ProcessName != event.TS2PHC {
			continue
		}
		before := len(data.Details)
		data.Details = slices.DeleteFunc(data.Details, func(detail *event.DataDetails) bool {
			return slices.Contains(inactivePHCs, t.deviceForInterface(detail.IFace))
		})
		if len(data.Details) != before {
			data.UpdateState()
			changed = true
		}
	}
	return changed
}

func (t *ts2phcTracker) events(ev event.Event, observation *event.TS2PHCObservation, policy ts2phcPolicy) []event.Event {
	t.skipPHC = policy.skipPHC
	if cycle := t.currentPollCycle; cycle != nil {
		if !policy.pollTimeouts {
			cycle.counted = false
			t.timeoutGroup = 0
		}
		for i := range cycle.tstamps {
			if cycle.tstamps[i].device == policy.skipPHC {
				cycle.tstamps[i].handled = true
			}
		}
	}
	switch {
	case observation.Role != "":
		return nil
	case observation.PollSuccessful:
		if cycle := t.currentPollCycle; cycle != nil {
			device := t.deviceForInterface(ev.IFace)
			for i := range cycle.tstamps {
				if cycle.tstamps[i].device == device {
					cycle.tstamps[i].handled = true
				}
			}
		}
	case observation.PollTimeout:
		if !policy.pollTimeouts {
			return nil
		}
		group, err := pollGroupFromTimestamp(observation.Timestamp)
		if err != nil {
			glog.Errorf("Invalid ts2phc timeout timestamp for %s: %v", ev.CfgName, err)
			return nil
		}
		return t.timeoutEvents(ev, group)
	case observation.PHCTstamp != nil && observation.PHCDevice != "":
		group, err := pollGroupFromTimestamp(observation.Timestamp)
		if err != nil {
			glog.Errorf("Invalid ts2phc timestamp for %s: %v", ev.CfgName, err)
			return nil
		}
		return t.timestampEvents(ev, observation, group, policy)
	}
	if observation.Offset == nil {
		return nil
	}
	state := observation.State
	if state != event.PTP_HOLDOVER && state != event.PTP_FREERUN &&
		t.config.Threshold.MaxOffsetThreshold > 0 && math.Abs(*observation.Offset) >= float64(t.config.Threshold.MaxOffsetThreshold) {
		state = event.PTP_FREERUN
	}
	var data event.EventData = &event.StateData{State: state}
	if !math.IsNaN(*observation.Offset) && !math.IsInf(*observation.Offset, 0) {
		offset := &event.OffsetData{State: state, Offset: int64(*observation.Offset)}
		if observation.NMEALocked != nil {
			locked := *observation.NMEALocked
			offset.NMEALocked = &locked
		}
		data = offset
	}
	ev.Data = data
	ev.IFace = t.canonicalInterface(ev.IFace)
	ev.WriteToLog = observation.NMEALocked != nil
	return []event.Event{ev}
}

func (t *ts2phcTracker) startRun(holder *DataHolder) bool {
	t.reset()
	holder.dataMu.Lock()
	defer holder.dataMu.Unlock()
	before := len(holder.data)
	holder.data = slices.DeleteFunc(holder.data, func(data *event.Data) bool {
		return data.ProcessName == event.TS2PHC
	})
	return len(holder.data) != before
}

func (t *ts2phcTracker) canonicalInterface(iface string) string {
	if configured := t.interfaceForDevice(t.deviceForInterface(iface)); configured.Name != "" {
		return configured.Name
	}
	return iface
}

func (t *ts2phcTracker) timestampEvents(ev event.Event, observation *event.TS2PHCObservation, group int, policy ts2phcPolicy) []event.Event {
	ts := tstamp{
		seconds: observation.PHCTstamp.Seconds, nanoseconds: observation.PHCTstamp.Nanoseconds,
		device: observation.PHCDevice, role: event.TS2PHCUnmanaged,
	}
	if iface := t.interfaceForDevice(ts.device); iface.Name != "" {
		ts.iface = iface.Name
		ts.role = t.roles[ts.device]
	}
	ts.handled = ts.role != event.TS2PHCSink || ts.device == policy.skipPHC
	var events []event.Event
	if cycle := t.currentPollCycle; cycle != nil {
		if cycle.shouldContain(ts, group) {
			cycle.lastSampleGroup = group
			// A duplicated print for the same PHC/edge is not a second sample.
			if !slices.ContainsFunc(cycle.tstamps, func(previous tstamp) bool { return previous.device == ts.device }) {
				cycle.tstamps = append(cycle.tstamps, ts)
			}
			return nil
		}
		events = t.unhandledEvents(ev, cycle)
		if policy.pollTimeouts {
			events = append(events, t.missingEvents(ev, cycle)...)
		}
	}
	t.currentPollCycle = &pollCycle{group: group, lastSampleGroup: group, tstamps: []tstamp{ts}}
	return events
}

func pollGroupFromTimestamp(timestamp string) (int, error) {
	wholeSeconds, _, _ := strings.Cut(timestamp, ".")
	return strconv.Atoi(wholeSeconds)
}

func (c *pollCycle) shouldContain(ts tstamp, group int) bool {
	for _, previous := range c.tstamps {
		if previous.device == ts.device && (previous.seconds != ts.seconds || previous.nanoseconds != ts.nanoseconds) {
			return false
		}
	}
	return len(c.tstamps) == 0 || group-c.lastSampleGroup <= 1
}

func (t *ts2phcTracker) timeoutEvents(ev event.Event, group int) []event.Event {
	cycle := t.currentPollCycle
	if cycle == nil {
		return nil // Startup/reset: no timestamp batch has started yet.
	}
	events := t.unhandledEvents(ev, cycle)
	// A timeout or a log-second boundary alone cannot close a partial batch.
	if group != cycle.group && (len(cycle.tstamps) == 0 || group > cycle.lastSampleGroup &&
		(cycle.counted && t.timeoutGroup != group || t.allSinksHandled(cycle))) {
		// Report only the cycle being closed. A newly opened empty cycle is a
		// candidate for the next PPS edge; this timeout is not evidence that edge
		// was missed (the pulse may arrive later in the same log second).
		events = appendTS2PHCEventsOnce(events, t.missingEvents(ev, cycle)...)
		t.currentPollCycle = &pollCycle{group: group, counted: true}
	}
	t.currentPollCycle.counted = true
	t.timeoutGroup = group
	return events
}

func appendTS2PHCEventsOnce(events []event.Event, missing ...event.Event) []event.Event {
	for _, candidate := range missing {
		if !slices.ContainsFunc(events, func(existing event.Event) bool { return existing.IFace == candidate.IFace }) {
			events = append(events, candidate)
		}
	}
	return events
}

func (t *ts2phcTracker) allSinksHandled(cycle *pollCycle) bool {
	for device, role := range t.roles {
		if role == event.TS2PHCSink && !slices.ContainsFunc(cycle.tstamps, func(ts tstamp) bool { return ts.device == device && ts.handled }) {
			return false
		}
	}
	return true
}

func (t *ts2phcTracker) unhandledEvents(ev event.Event, cycle *pollCycle) []event.Event {
	var events []event.Event
	for i := range cycle.tstamps {
		ts := &cycle.tstamps[i]
		if ts.handled || ts.role != event.TS2PHCSink {
			continue
		}
		events = append(events, t.failureEvent(ev, ts.device, computeTSOffset(ts, cycle)))
		ts.handled = true
		glog.Infof("Missing ts2phc offset: FREERUN for unmatched timestamp on %s (%s)", ts.iface, ts.device)
	}
	return events
}

func (t *ts2phcTracker) missingEvents(ev event.Event, cycle *pollCycle) []event.Event {
	var events []event.Event
	seen := make(map[string]bool)
	for _, iface := range t.config.Interfaces {
		device := iface.PHCDevice
		if seen[device] || t.roles[device] != event.TS2PHCSink {
			continue
		}
		seen[device] = true
		if !slices.ContainsFunc(cycle.tstamps, func(ts tstamp) bool { return ts.device == device }) {
			events = append(events, t.failureEvent(ev, device, math.NaN()))
			glog.Infof("ts2phc poll cycle timeout: FREERUN for missing sink %s (%s)", iface.Name, device)
		}
	}
	return events
}

func (t *ts2phcTracker) failureEvent(ev event.Event, device string, offset float64) event.Event {
	iface := t.interfaceForDevice(device)
	ev.IFace = iface.Name
	ev.Data = &event.StateData{State: event.PTP_FREERUN}
	ev.WriteToLog = iface.Source == event.GNSS
	if !math.IsNaN(offset) {
		data := &event.OffsetData{State: event.PTP_FREERUN, Offset: int64(offset)}
		if iface.Source == event.GNSS {
			locked := true
			data.NMEALocked = &locked
		}
		ev.Data = data
	}
	return ev
}

func (c *pollCycle) sourceTimestamps() []tstamp {
	var sources []tstamp
	for _, ts := range c.tstamps {
		if ts.role == event.TS2PHCSource {
			sources = append(sources, ts)
		}
	}
	return sources
}

// maxOffset returns the signed other-minus-target offset furthest from zero,
// preserving integer nanosecond precision even at realistic epoch values.
func maxOffset(ts *tstamp, timestamps []tstamp) float64 {
	measured := false
	var offset int64
	for _, other := range timestamps {
		if ts.device == other.device {
			continue
		}
		delta := (other.seconds-ts.seconds)*1e9 + other.nanoseconds - ts.nanoseconds
		if !measured || absInt64(delta) > absInt64(offset) {
			offset = delta
		}
		measured = true
	}
	if !measured {
		return math.NaN()
	}
	return float64(offset)
}

func absInt64(value int64) uint64 {
	if value < 0 {
		return uint64(-(value + 1)) + 1
	}
	return uint64(value)
}

func computeTSOffset(ts *tstamp, cycle *pollCycle) float64 {
	if cycle == nil || ts == nil {
		return math.NaN()
	}
	if offset := maxOffset(ts, cycle.sourceTimestamps()); !math.IsNaN(offset) {
		return offset
	}
	return math.Abs(maxOffset(ts, cycle.tstamps))
}
