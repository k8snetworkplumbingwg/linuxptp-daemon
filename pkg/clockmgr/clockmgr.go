package clockmgr

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/alias"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/debug"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"
	parserconstants "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	configLabel  = "config"
	fromLabel    = "from"
	nodeLabel    = "node"
	processLabel = "process"
	ifaceLabel   = "iface"
)

// ClockManager ... event handler to process events
type ClockManager struct {
	clockManagementMu sync.Mutex
	nodeName          string
	events            chan event.Event
	metricCache       map[metricKey]metricEntry
	registeredGauges  map[string]*prometheus.GaugeVec
	offsetMetric      *prometheus.GaugeVec
	clockMetric       *prometheus.GaugeVec
	clockClassMetric  *prometheus.GaugeVec
	haMetric          *prometheus.GaugeVec
	clocks            map[string]clock.Clock // cfgName → Clock
	osClock           clock.OsClock
	ipcCache          *ipc.Cache
	// applyingProfiles is set while applyNodePTPProfiles is tearing down /
	// restarting processes. When true, T-BC/T-TSC events are skipped so
	// teardown races cannot emit spurious state transitions.
	applyingProfiles atomic.Bool
	// syncStatusCh is signalled (non-blocking) whenever a clock state transitions.
	syncStatusCh chan struct{}
	// statsUpdates holds the latest complete snapshot for ProcessManager. All
	// publishers are serialized by clockManagementMu; old pending snapshots
	// are replaced so a slow consumer cannot block clock processing.
	statsUpdates   chan process.EventStats
	osClockConfigs map[string]struct{}
	// Per-clock source state labels follow the owning clock's metric snapshot.
	sourceStateIfaces map[sourceStateKey]map[string]struct{}
}

// ConfigureSourceQualification sets policy for the active upstream PTP input of
// cfg's ptp4l instance, not aggregate lock or the remote GM's time reference.
// Configure before process startup; runtime observations use the event channel.
func (m *ClockManager) ConfigureSourceQualification(cfg string, ports []string, policy utils.QualificationConfig) (uint64, error) {
	m.clockManagementMu.Lock()
	defer m.clockManagementMu.Unlock()
	clk, ok := m.clocks[cfg].(*clock.TBC)
	if !ok {
		return 0, fmt.Errorf("source qualification requires a T-BC clock for %s", cfg)
	}
	generation, err := clk.ConfigureSourceQualification(ports, policy)
	m.publishStats()
	return generation, err
}

// SourceQualification snapshots the upstream identity/epoch for a new process
// run. Startup conditions continue to consume the retained stats channel.
func (m *ClockManager) SourceQualification(cfg string) (event.SourceQualification, bool) {
	m.clockManagementMu.Lock()
	defer m.clockManagementMu.Unlock()
	if clk, ok := m.clocks[cfg].(*clock.TBC); ok {
		return clk.SourceQualification(cfg)
	}
	return event.SourceQualification{}, false
}

type metricKey struct {
	cfgName  string
	process  string
	iface    string
	dataType event.ValueType
}

type metricEntry struct {
	gauge  *prometheus.GaugeVec
	labels prometheus.Labels
}

type sourceStateKey struct {
	clockName string
	source    event.EventSource
}

// clockConfigDataProvider lets composite clocks return member-specific data
// without adding config-specific behavior to every Clock implementation.
type clockConfigDataProvider interface {
	ProcessDataForConfig(cfgName string) []*event.Data
}

// ConfigureClock installs setup-time configuration before processes start.
func (m *ClockManager) ConfigureClock(configName string, config event.ClockConfiguration) error {
	m.clockManagementMu.Lock()
	defer m.clockManagementMu.Unlock()
	clk := m.GetClock(configName)
	configurable, ok := clk.(clock.Configurable)
	if !ok {
		return fmt.Errorf("clock %s does not support configuration", configName)
	}
	metrics, err := configurable.Configure(configName, config)
	if err != nil {
		return err
	}
	m.syncSourceMetrics(clk.ConfigName(), metrics)
	return nil
}

// SetHAMetric injects the openshift_ptp_ha_profile_status gauge. It is set
// separately from Init because the gauge is created and registered by the daemon
// package, which clockmgr cannot import.
func (m *ClockManager) SetHAMetric(g *prometheus.GaugeVec) {
	m.haMetric = g
}

// SetApplying marks whether a PTP profile apply is in progress.
func (m *ClockManager) SetApplying(v bool) {
	m.applyingProfiles.Store(v)
}

// IsApplying reports whether a PTP profile apply is in progress.
func (m *ClockManager) IsApplying() bool {
	return m.applyingProfiles.Load()
}

// SyncStatusUpdateCh returns a channel that receives a signal (non-blocking send)
// whenever a clock state transitions. Missed signals are coalesced.
func (m *ClockManager) SyncStatusUpdateCh() <-chan struct{} {
	return m.syncStatusCh
}

func (m *ClockManager) signalSyncStatus() {
	select {
	case m.syncStatusCh <- struct{}{}:
	default:
	}
}

// StatsUpdatesCh returns the snapshot channel for the single ProcessManager
// consumer. It is not a subscription: the channel retains the latest snapshot
// even before the manager starts, and is never closed by ClockManager.
func (m *ClockManager) StatsUpdatesCh() <-chan process.EventStats {
	return m.statsUpdates
}

// publishStats pushes a detached snapshot after every evaluated sample/reset,
// including unchanged states. Call with clockManagementMu held (or during Init).
func (m *ClockManager) publishStats() {
	requests := make([]process.StatsRequest, 0, len(m.clocks)+len(m.osClockConfigs))
	for cfg := range m.clocks {
		requests = append(requests, process.StatsRequest{ClockID: cfg})
	}
	for cfg := range m.osClockConfigs {
		if _, exists := m.clocks[cfg]; !exists {
			requests = append(requests, process.StatsRequest{ClockID: cfg})
		}
	}
	stats := m.snapshotStats(requests)
	select {
	case m.statsUpdates <- stats:
	default:
		select {
		case <-m.statsUpdates:
		default:
		}
		// Only this lock-serialized publisher writes to the capacity-one
		// channel. After removing the old value, this send cannot block.
		m.statsUpdates <- stats
	}
}

// Init ... initialize event manager
func Init(nodeName string, processChannel chan event.Event, offsetMetric *prometheus.GaugeVec, clockMetric *prometheus.GaugeVec, clockClassMetric *prometheus.GaugeVec, ipcCache *ipc.Cache) *ClockManager {
	m := &ClockManager{
		nodeName:          nodeName,
		events:            processChannel,
		metricCache:       map[metricKey]metricEntry{},
		registeredGauges:  map[string]*prometheus.GaugeVec{},
		clockMetric:       clockMetric,
		offsetMetric:      offsetMetric,
		clockClassMetric:  clockClassMetric,
		clocks:            map[string]clock.Clock{},
		osClock:           clock.OsClock{State: event.PTP_FREERUN, E3State: event.PTP_NOTSET, IFace: parserconstants.ClockRealTime},
		ipcCache:          ipcCache,
		syncStatusCh:      make(chan struct{}, 1),
		statsUpdates:      make(chan process.EventStats, 1),
		osClockConfigs:    make(map[string]struct{}),
		sourceStateIfaces: make(map[sourceStateKey]map[string]struct{}),
	}
	m.publishStats()
	return m
}

// ConfigureOSClockFilter applies the resolved phc2sys profile settings.
func (m *ClockManager) ConfigureOSClockFilter(cfg clock.OSClockFilterConfig) {
	m.clockManagementMu.Lock()
	defer m.clockManagementMu.Unlock()
	m.osClock.ConfigureFilter(cfg)
}

// AddClock takes ownership of a given Clock
// pmcClient may be nil for clock types that do not use PMC (e.g. BC, OC).
func (m *ClockManager) AddClock(clk clock.Clock) error {
	clk.SetIPC(m.sendIPC)
	clk.SetEventLoopbackFunc(m.sendEvent)
	m.clockManagementMu.Lock()
	defer m.clockManagementMu.Unlock()
	clk.SetOsClock(&m.osClock)
	// Register every config name the clock accepts, including aliases and member configs.
	for _, cfg := range clk.ConfigNames() {
		if prev, exists := m.clocks[cfg]; exists {
			glog.Warningf("AddClock: replacing existing %s clock for config %s", prev.ClockType(), cfg)
		}
		m.clocks[cfg] = clk
	}
	glog.Infof("AddClock: registered %s clock for config %s", clk.ClockType(), clk.ConfigName())
	m.publishStats()
	return nil
}

// snapshotStats builds a detached snapshot while clockManagementMu is held.
func (m *ClockManager) snapshotStats(requests []process.StatsRequest) process.EventStats {
	stats := process.EventStats{
		Windows:         make(map[string]map[event.EventSource]utils.ROWindow),
		States:          make(map[string]map[event.EventSource]event.PTPState),
		ClockStates:     make(map[string]event.PTPState),
		OSClockState:    m.osClock.State,
		OSClockE3State:  m.osClock.E3State,
		SourceQualified: make(map[string]bool),
	}
	for _, req := range requests {
		var data []*event.Data
		if clk := m.GetClock(req.ClockID); clk != nil {
			if tbc, ok := clk.(*clock.TBC); ok {
				if result, exists := tbc.SourceQualification(req.ClockID); exists && (req.Source == "" || req.Source == result.SourceType) {
					stats.SourceQualified[req.ClockID] = result.Qualified && !m.IsApplying()
				}
			}
			// HA member config names resolve to the HA composite in m.clocks.
			// Its inherited ProcessData contains only composite/OS data, not
			// the members' ptp4l states and windows. Preserve those per-member
			// samples so phc2sys startup can qualify any healthy HA member;
			// otherwise its offset-window conditions never become ready.
			if provider, ok := clk.(clockConfigDataProvider); ok {
				data = provider.ProcessDataForConfig(req.ClockID)
			} else {
				data = clk.ProcessData()
			}
			stats.ClockStates[req.ClockID] = clk.GetState()
		} else {
			data = m.osClock.ProcessData()
		}
		for _, d := range data {
			if d == nil || (req.Source != "" && d.ProcessName != req.Source) {
				continue
			}
			if stats.States[req.ClockID] == nil {
				stats.States[req.ClockID] = make(map[event.EventSource]event.PTPState)
				stats.Windows[req.ClockID] = make(map[event.EventSource]utils.ROWindow)
			}
			stats.States[req.ClockID][d.ProcessName] = d.State
			if d.Window != nil {
				stats.Windows[req.ClockID][d.ProcessName] = d.Window.Snapshot()
			}
		}
	}
	return stats
}

// RemoveAllClocks tears down all registered clocks and cleans up associated state.
func (m *ClockManager) RemoveAllClocks() {
	m.clockManagementMu.Lock()
	m.osClock.Reset()

	for cfgName := range m.clocks {
		m.unregisterMetrics(cfgName, "")
	}
	for key := range m.sourceStateIfaces {
		m.clearSourceStateMetrics(key)
	}
	m.clocks = map[string]clock.Clock{}
	m.osClockConfigs = make(map[string]struct{})
	m.publishStats()
	m.clockManagementMu.Unlock()

	debug.ClearState()
}

// GetClock returns the Clock registered for the given config name, or nil.
func (m *ClockManager) GetClock(cfgName string) clock.Clock {
	return m.clocks[cfgName]
}

// sendIPC sends an IPC message
func (m *ClockManager) sendIPC(msg ipc.Message) {
	if m.ipcCache != nil {
		m.ipcCache.Send(msg)
	}
}

// sendEvent posts an event back onto the processing channel so it is handled by
// the serialized ProcessEvents loop. It is used by clocks to feed asynchronous
// PMC results (e.g. the T-BC downstream announce) back to their state machines
// without mutating clock state off-loop. Callers must invoke it from a
// goroutine, never from the loop itself, to avoid blocking on a full channel.
func (m *ClockManager) sendEvent(ev event.Event) {
	m.events <- ev
}

// GetUtcOffset returns the current UTC offset.
func (m *ClockManager) GetUtcOffset() int {
	return leap.GetUtcOffset()
}

// handleOSClockEvent lets OsClock accept its own events, derives E3, and notifies
// the registered clocks when the disciplining state changes. It returns whether
// the event was handled, independently of whether the state changed.
func (m *ClockManager) handleOSClockEvent(ev event.Event) bool {
	previous := m.osClock.State
	if !m.osClock.AddEvent(ev) {
		return false
	}
	m.osClockConfigs[ev.CfgName] = struct{}{}
	derived := m.emitOSClockState()
	if m.osClock.State == previous {
		return true
	}
	m.signalSyncStatus()
	glog.V(4).Infof("OS clock: disciplining %s -> E3 %s (e1 %s)", m.osClock.State, derived, m.worstClockState())
	for _, clk := range m.clocks {
		clk.SystemClockUpdate()
	}
	return true
}

// worstClockState returns the most degraded state across registered clocks
// (FREERUN > HOLDOVER > LOCKED), or NOTSET if all states are unset/unknown.
func (m *ClockManager) worstClockState() event.PTPState {
	worst := event.PTP_NOTSET
	for _, clk := range m.clocks {
		switch clk.GetState() {
		case event.PTP_FREERUN:
			return event.PTP_FREERUN // Nothing can be worse.
		case event.PTP_HOLDOVER:
			worst = event.PTP_HOLDOVER
		case event.PTP_LOCKED:
			if worst == event.PTP_NOTSET {
				worst = event.PTP_LOCKED
			}
		}
	}
	return worst
}

// emitOSClockState derives the node-wide E3 OS-clock state as worst-of(E1,
// phc2sys) and emits an os_clock_state IPC when it changes. Returns the derived
// state. Assumes clockManagementMu is held.
func (m *ClockManager) emitOSClockState() event.PTPState {
	e1 := m.worstClockState()
	derived := clock.DeriveOSClockState(e1, m.osClock.State)
	if len(m.clocks) == 0 {
		derived = m.osClock.State
	}
	if derived == event.PTP_NOTSET || derived == m.osClock.E3State {
		return derived
	}
	m.osClock.E3State = derived
	glog.V(4).Infof("OS clock E3: %s (e1 %s, phc2sys %s)", derived, e1, m.osClock.State)
	m.sendIPC(ipc.Message{
		Type:   ipc.TypeOSClockState,
		IFace:  m.osClock.IFace,
		Values: ipc.StateValue{State: event.PtpStateToIPCState(derived), Offset: m.osClock.Offset},
	})
	return derived
}

// ProcessEvents loops until
func (m *ClockManager) ProcessEvents(ctx context.Context) {
	glog.Info("starting state monitoring...")
	for {
		select {
		case ev, ok := <-m.events:
			if !ok {
				return
			}
			m.ProcessEvent(ev)
		case <-ctx.Done():
			return
		}
	}
}

// ProcessEvent evaluates one event. Production callers forward events to the
// ProcessEvents channel; the resulting snapshot is pushed to ProcessManager.
// All clock mutation is serialized by clockManagementMu.
func (m *ClockManager) ProcessEvent(ev event.Event) {
	m.clockManagementMu.Lock()
	defer m.clockManagementMu.Unlock()
	if observation, ok := ev.Data.(*event.PTPSourceObservation); ok {
		result := event.SourceQualification{}
		if clk, exists := m.clocks[ev.CfgName].(*clock.TBC); exists && !m.IsApplying() {
			result = clk.ObserveSource(ev, observation)
		}
		m.publishStats()
		// Rejected observations also get a reply; a removed/replaced clock must
		// not strand its old scanner. Never wait for hardware or a receiver.
		select {
		case observation.Reply <- result:
		default:
		}
		return
	}
	if ev.Reset {
		m.reset(ev)
		m.publishStats()
		return
	}

	// OsClock owns event acceptance. Other events continue to their clock by
	// config name, including HA source-selection events.
	if m.handleOSClockEvent(ev) {
		m.publishStats()
		return
	}

	// TODO: Move to better identifiers? Having to do this translation here is odd
	lookupName := ev.CfgName
	if ev.Source == event.SYNCE {
		lookupName = strings.Replace(ev.CfgName, "synce4l", "ptp4l", 1)
	}

	clk := m.GetClock(lookupName)
	if clk == nil {
		glog.Warningf("ProcessEvents: no clock registered for %s, skipping event", lookupName)
		return
	}

	reportState := true
	if m.IsApplying() {
		if policy, ok := clk.(clock.ApplyingEventPolicy); ok {
			accept, report := policy.DuringApply(ev)
			if !accept {
				return
			}
			reportState = report
		}
	}
	prevState := clk.GetState()
	result := clk.AddEvent(ev)
	if result.Ignored {
		return
	}
	m.syncSourceMetrics(clk.ConfigName(), result.Metrics)
	if !reportState {
		m.reportEvents(result.Events)
		m.publishStats()
		return
	}
	clockState := result.SyncState
	if prevState != event.PTP_NOTSET && prevState != clockState.State {
		m.signalSyncStatus()
	}
	sample, available := clk.ClockOffsetMetric()
	m.updateClockOffsetMetric(clk.ConfigName(), string(clk.ClockType()), sample, available)
	if clockState.LeadingIFace != "" && clockState.LeadingIFace != event.LEADING_INTERFACE_UNKNOWN {
		// BC/OC clock_state is scraped as process="ptp4l" (ptp4l is the
		// servo), whereas GM/T-BC report under their clock-type label. The
		// event's clock type is checked too: an HA clock is a composite whose
		// member events carry ClockType=BC, and those must still report as
		// ptp4l like a standalone BC follower.
		process := clockStateMetricProcess(clk.ClockType(), ev.ClockType)
		m.updateClockStateMetrics(clockState.State, process, alias.GetAlias(clockState.LeadingIFace))
	}
	// Use the clock class from the event's returned state, not clk.ClockClass().
	// For a composite HA clock, clk.ClockClass() is the active member's class,
	// but the metric is keyed per member config (lookupName), so each member
	// must report its own class. For GM/TBC/BC the returned state carries the
	// same class clk.ClockClass() would, so this is equivalent for them.
	m.updateClockClassMetrics(clk.ClockClassMetricConfigName(lookupName), clockState.ClockClass)
	m.updateHAProfileMetrics(clockState.HAProfileStatus)
	m.reportEvents(result.Events)
	// A clock (E1) transition may move the derived E3 os_clock_state.
	m.emitOSClockState()
	m.publishStats()
}

func (m *ClockManager) reportEvents(reports []clock.EventReport) {
	for _, report := range reports {
		ev := report.Event
		if ev.WriteToLog {
			if logData := ev.GetLogData(); logData != "" {
				fmt.Printf("%s", logData)
			}
		}
		m.updateMetrics(report)
	}
}

// clockStateMetricProcess selects the process label for a clock-state metric.
// Synthetic events such as asynchronous T-BC PMC completions may omit
// ClockType, so fall back to the clock that accepted the event. HA member
// events retain their member type so BC/OC continue to report as ptp4l.
func clockStateMetricProcess(clockType, eventClockType event.ClockType) string {
	if eventClockType == event.ClockUnset {
		eventClockType = clockType
	}
	if clockType == event.BC || clockType == event.OC ||
		eventClockType == event.BC || eventClockType == event.OC {
		return string(event.PTP4l)
	}
	return string(eventClockType)
}

// reset handles a reset event
func (m *ClockManager) reset(ev event.Event) {
	debug.ClearState()
	if m.ipcCache != nil {
		m.ipcCache.Clear()
	}
	var metrics []clock.SourceMetrics
	var clockName string
	clk := m.GetClock(ev.CfgName)
	if clk != nil {
		if resetter, ok := clk.(interface{ ResetSource(event.EventSource) }); ok {
			resetter.ResetSource(ev.Source)
		} else {
			clk.Reset()
		}
		clockName = clk.ConfigName()
		m.deleteClockOffsetMetrics(clockName, string(clk.ClockType()))
		m.clearClockSourceStateMetrics(clockName)
		if provider, ok := clk.(clock.ResetMetricProvider); ok {
			metrics = provider.MetricsAfterReset(ev.CfgName, ev.Source)
		}
	}
	m.osClock.E3State = event.PTP_NOTSET
	if len(metrics) == 0 {
		processName := string(ev.Source)
		if ev.ResetAllMetrics {
			processName = ""
		}
		m.unregisterMetrics(ev.CfgName, processName)
		return
	}
	m.syncSourceMetrics(clockName, metrics)
}

// syncSourceMetrics applies the source-owned metric series reported by a clock.
func (m *ClockManager) syncSourceMetrics(clockName string, snapshots []clock.SourceMetrics) {
	for _, snapshot := range snapshots {
		if snapshot.ClearOtherSources {
			for key, entry := range m.metricCache {
				if key.cfgName == snapshot.ConfigName && key.process != string(snapshot.Source) {
					m.deleteMetric(key, entry)
				}
			}
		}
		if snapshot.Offsets != nil {
			m.syncExpectedOffsetMetrics(snapshot.ConfigName, snapshot.Source, snapshot.Offsets)
		}
		if snapshot.States != nil {
			m.syncSourceStateMetrics(clockName, snapshot.Source, snapshot.States)
		}
	}
}

func (m *ClockManager) syncExpectedOffsetMetrics(configName string, source event.EventSource, offsets *clock.OffsetMetrics) {
	expected := make(map[string]bool)
	for _, iface := range offsets.Interfaces {
		expected[alias.GetAlias(iface)] = true
	}
	for key, entry := range m.metricCache {
		if key.cfgName != configName || key.process != string(source) {
			continue
		}
		if key.dataType == event.OFFSET && expected[key.iface] {
			if offsets.Invalidate {
				m.invalidateOffsetMetric(configName, source, offsets.From, key.iface)
			}
		} else if key.dataType == event.OFFSET || offsets.Invalidate {
			m.deleteMetric(key, entry)
		}
	}
	for iface := range expected {
		key := metricKey{cfgName: configName, process: string(source), iface: iface, dataType: event.OFFSET}
		if _, exists := m.metricCache[key]; !exists {
			m.invalidateOffsetMetric(configName, source, offsets.From, iface)
		}
	}
}

// invalidateOffsetMetric represents an unavailable measurement only at the
// Prometheus boundary; NaN is never converted to an integer event offset.
// iface is already normalized to its Prometheus alias by the caller.
func (m *ClockManager) invalidateOffsetMetric(configName string, source event.EventSource, from, iface string) {
	if m.offsetMetric == nil || iface == "" {
		return
	}
	if from == "" {
		from = string(source)
	}
	labels := prometheus.Labels{fromLabel: from, nodeLabel: m.nodeName, processLabel: string(source), ifaceLabel: iface}
	m.offsetMetric.With(labels).Set(math.NaN())
	key := metricKey{cfgName: configName, process: string(source), iface: iface, dataType: event.OFFSET}
	m.metricCache[key] = metricEntry{gauge: m.offsetMetric, labels: labels}
}

func (m *ClockManager) syncSourceStateMetrics(clockName string, source event.EventSource, states *clock.StateMetrics) {
	if m.clockMetric == nil {
		return
	}
	current := make(map[string]struct{})
	for _, detail := range states.Details {
		if detail.IFace == "" || (detail.State != event.PTP_LOCKED && detail.State != event.PTP_FREERUN && detail.State != event.PTP_HOLDOVER) {
			continue
		}
		iface := alias.GetAlias(detail.IFace)
		current[iface] = struct{}{}
		m.updateClockStateMetrics(detail.State, string(source), iface)
	}
	key := sourceStateKey{clockName: clockName, source: source}
	for iface := range m.sourceStateIfaces[key] {
		if _, exists := current[iface]; !exists {
			m.clockMetric.Delete(prometheus.Labels{processLabel: string(source), nodeLabel: m.nodeName, ifaceLabel: iface})
		}
	}
	if m.sourceStateIfaces == nil {
		m.sourceStateIfaces = make(map[sourceStateKey]map[string]struct{})
	}
	m.sourceStateIfaces[key] = current
}

func (m *ClockManager) clearSourceStateMetrics(key sourceStateKey) {
	if m.clockMetric != nil {
		for iface := range m.sourceStateIfaces[key] {
			m.clockMetric.Delete(prometheus.Labels{processLabel: string(key.source), nodeLabel: m.nodeName, ifaceLabel: iface})
		}
	}
	delete(m.sourceStateIfaces, key)
}

func (m *ClockManager) clearClockSourceStateMetrics(clockName string) {
	for key := range m.sourceStateIfaces {
		if key.clockName == clockName {
			m.clearSourceStateMetrics(key)
		}
	}
}

// updateClockStateMetrics should be used to update metrics when a clock changes state
func (m *ClockManager) updateClockStateMetrics(state event.PTPState, process, iFace string) {
	if m.clockMetric == nil {
		return
	}
	if !utils.CheckMetricSanity("ClockState", process, iFace) {
		return
	}
	labels := prometheus.Labels{
		processLabel: process, nodeLabel: m.nodeName, ifaceLabel: iFace}
	switch state {
	case event.PTP_LOCKED:
		m.clockMetric.With(labels).Set(event.ClockStateLocked)
	case event.PTP_HOLDOVER:
		m.clockMetric.With(labels).Set(event.ClockStateHoldover)
	default:
		m.clockMetric.With(labels).Set(event.ClockStateFreerun)
	}
}

// updateClockOffsetMetric exports an optional clock-provided aggregate offset.
// It is separate from raw process samples emitted by updateMetrics.
func (m *ClockManager) updateClockOffsetMetric(cfgName, process string, sample clock.OffsetSample, available bool) {
	if !available || sample.IFace == "" || sample.IFace == event.LEADING_INTERFACE_UNKNOWN {
		m.deleteClockOffsetMetrics(cfgName, process)
		return
	}
	if m.offsetMetric == nil {
		return
	}

	iface := alias.GetAlias(sample.IFace)
	if !utils.CheckMetricSanity("PTPMetrics", process, iface) {
		m.deleteClockOffsetMetrics(cfgName, process)
		return
	}

	key := metricKey{cfgName: cfgName, process: process, iface: iface, dataType: event.OFFSET}
	labels := prometheus.Labels{fromLabel: process, nodeLabel: m.nodeName, processLabel: process, ifaceLabel: iface}
	if entry, found := m.metricCache[key]; found {
		entry.labels = labels
		entry.gauge.With(labels).Set(float64(sample.Offset))
		m.metricCache[key] = entry
		return
	}

	m.offsetMetric.With(labels).Set(float64(sample.Offset))
	m.metricCache[key] = metricEntry{gauge: m.offsetMetric, labels: labels}
}

// deleteClockOffsetMetrics removes cached aggregate offset series for cfgName
// and process.
func (m *ClockManager) deleteClockOffsetMetrics(cfgName, process string) {
	for key, entry := range m.metricCache {
		if key.cfgName != cfgName || key.process != process || key.dataType != event.OFFSET {
			continue
		}
		if entry.gauge != nil {
			entry.gauge.Delete(entry.labels)
		}
		delete(m.metricCache, key)
	}
}

// updateHAProfileMetrics sets the openshift_ptp_ha_profile_status gauge for each
// HA member profile (1 = ACTIVE, 0 = INACTIVE). status is nil for every non-HA
// clock, in which case this is a no-op. The label set matches the legacy metric:
// process="phc2sys", node, profile.
func (m *ClockManager) updateHAProfileMetrics(status map[string]bool) {
	if m.haMetric == nil || status == nil {
		return
	}
	for profile, active := range status {
		value := 0.0
		if active {
			value = 1.0
		}
		m.haMetric.With(prometheus.Labels{
			processLabel: string(event.PHC2SYS), nodeLabel: m.nodeName, "profile": profile,
		}).Set(value)
	}
}

// updateClockClassMetrics updates the clock class gauge for a config. The class
// is emitted under the ptp4l config name and process (matching downstream
// consumers regardless of the source event); an uninitialized (0) class is
// skipped since it means the clock has not yet determined its class.
func (m *ClockManager) updateClockClassMetrics(cfgName string, clockClass fbprotocol.ClockClass) {
	if m.clockClassMetric == nil {
		return
	}
	if clockClass == protocol.ClockClassUninitialized {
		return
	}
	m.clockClassMetric.With(prometheus.Labels{
		processLabel: "ptp4l", nodeLabel: m.nodeName, configLabel: cfgName}).Set(float64(clockClass))
}

// convertToFloat converts an event field to float64.
// Bool values map to 1 (true) and 0 (false).
func convertToFloat(value interface{}) (float64, bool) {
	switch val := value.(type) {
	case int64:
		return float64(val), true
	case float64:
		return val, true
	case bool:
		if val {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

// updateMetrics exports clock-reported values and event payload metrics.
// Metrics are cached by (cfgName, process, iface, dataType) to avoid re-registering.
func (m *ClockManager) updateMetrics(report clock.EventReport) {
	if m.offsetMetric == nil && len(report.MetricValues) == 0 {
		return
	}
	ev := report.Event
	iface := alias.GetAlias(ev.IFace)
	if report.OffsetUnavailable {
		m.invalidateOffsetMetric(ev.CfgName, ev.Source, report.OffsetSource, iface)
	}

	var processData map[event.ValueType]interface{}
	switch data := ev.Data.(type) {
	case *event.GNSSData:
		processData = map[event.ValueType]interface{}{
			event.GPS_STATUS: data.GPSStatus,
			event.OFFSET:     data.Offset,
		}
	case *event.OffsetData:
		processData = map[event.ValueType]interface{}{
			event.OFFSET: data.Offset,
		}
		if data.NMEALocked != nil {
			processData[event.NMEA_STATUS] = *data.NMEALocked
		}
	case *event.DPLLData:
		processData = map[event.ValueType]interface{}{}
		if data.Offset != nil {
			processData[event.OFFSET] = *data.Offset
		}
		if data.PhaseStatus != nil {
			processData[event.PHASE_STATUS] = *data.PhaseStatus
		}
		if data.FrequencyStatus != nil {
			processData[event.FREQUENCY_STATUS] = *data.FrequencyStatus
		}
	case *event.StateData:
		// State-only clock failures may carry an auxiliary status metric, but
		// must never synthesize an offset measurement.
		processData = make(map[event.ValueType]interface{})
	default:
		return
	}
	for valueType, value := range report.MetricValues {
		processData[valueType] = value
	}

	for dataType, value := range processData {
		if dataType == event.OFFSET && report.OffsetUnavailable {
			continue
		}
		dataValue, ok := convertToFloat(value)
		if !ok {
			continue
		}

		pName := string(ev.Source)
		if dataType == event.OFFSET && report.OffsetSource != "" {
			pName = report.OffsetSource
		}

		key := metricKey{
			cfgName:  ev.CfgName,
			process:  string(ev.Source),
			iface:    iface,
			dataType: dataType,
		}

		labels := prometheus.Labels{fromLabel: pName, nodeLabel: m.nodeName,
			processLabel: string(ev.Source), ifaceLabel: iface}

		if entry, found := m.metricCache[key]; found {
			entry.labels = labels
			entry.gauge.With(labels).Set(dataValue)
			m.metricCache[key] = entry
		} else {
			var gauge *prometheus.GaugeVec
			metricName := getMetricName(dataType)

			if dataType == event.OFFSET {
				gauge = m.offsetMetric
			} else if existing, exists := m.registeredGauges[metricName]; exists {
				gauge = existing
			} else {
				gauge = prometheus.NewGaugeVec(
					prometheus.GaugeOpts{
						Namespace: event.PTPNamespace,
						Subsystem: event.PTPSubsystem,
						Name:      metricName,
						Help:      event.ValueTypeHelpTxt[dataType],
					}, []string{fromLabel, nodeLabel, processLabel, ifaceLabel})
				glog.Infof("trying to register metrics %s for %s", metricName, dataType)
				registerMetrics(gauge)
				m.registeredGauges[metricName] = gauge
			}

			gauge.With(labels).Set(dataValue)
			m.metricCache[key] = metricEntry{gauge: gauge, labels: labels}
		}
	}
}

func registerMetrics(m *prometheus.GaugeVec) {
	defer func() {
		if err := recover(); err != nil {
			glog.Errorf("restored from registering metrics: %s", err)
		}
	}()
	prometheus.MustRegister(m)
}

func (m *ClockManager) unregisterMetrics(configName string, processName string) {
	for key, entry := range m.metricCache {
		if key.cfgName == configName && (processName == "" || key.process == processName) {
			m.deleteMetric(key, entry)
		}
	}
}

func (m *ClockManager) deleteMetric(key metricKey, entry metricEntry) {
	if entry.gauge != nil {
		entry.gauge.Delete(entry.labels)
	}
	delete(m.metricCache, key)
}

func getMetricName(valueType event.ValueType) string {
	if strings.HasSuffix(string(valueType), string(event.OFFSET)) {
		return fmt.Sprintf("%s_%s", valueType, "ns")
	}
	return string(valueType)
}
