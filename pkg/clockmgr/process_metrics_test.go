package clockmgr

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/alias"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	parserconstants "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// requireGaugeValue checks an exported series without creating it via With.
func requireGaugeValue(t *testing.T, gauge *prometheus.GaugeVec, labels prometheus.Labels, want float64) {
	t.Helper()
	registry := prometheus.NewRegistry()
	registry.MustRegister(gauge)
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		for _, metric := range family.Metric {
			if len(metric.Label) != len(labels) {
				continue
			}
			matches := true
			for _, label := range metric.Label {
				value, ok := labels[label.GetName()]
				if !ok || value != label.GetValue() {
					matches = false
					break
				}
			}
			if matches {
				require.Equal(t, want, metric.GetGauge().GetValue())
				return
			}
		}
	}
	t.Fatalf("metric series not exported: %v", labels)
}

func newProcessMetricManager(t *testing.T, clockType event.ClockType) (*ClockManager, *prometheus.GaugeVec) {
	t.Helper()
	gauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_process_clock_state"},
		[]string{processLabel, nodeLabel, ifaceLabel})
	// Process state must be exported even without an offset collector.
	cm := Init("testnode", make(chan event.Event), nil, gauge, nil, nil)
	var clk clock.Clock
	var err error
	switch clockType {
	case event.GM:
		clk, err = clock.NewGM(testClockmgrPtp4lConfig, func() int { return 37 }, &pmc.MockClient{})
	case event.TBC:
		clk, err = clock.NewTBC(testClockmgrPtp4lConfig, func() int { return 37 }, &pmc.MockClient{}, "")
	default:
		t.Fatalf("unsupported test clock type %s", clockType)
	}
	require.NoError(t, err)
	require.NoError(t, cm.AddClock(clk))
	return cm, gauge
}

func TestProcessClockStateMetrics(t *testing.T) {
	for _, clockType := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(clockType), func(t *testing.T) {
			cm, gauge := newProcessMetricManager(t, clockType)
			const iface = "process-metric-port"
			alias.SetAlias(iface, "process-metric-alias")
			t.Cleanup(alias.ClearAliases)
			labels := prometheus.Labels{processLabel: "gnss", nodeLabel: "testnode", ifaceLabel: "process-metric-alias"}
			tests := []struct {
				source event.EventSource
				data   event.EventData
				want   float64
			}{
				{event.GNSS, &event.GNSSData{GPSStatus: 2}, event.ClockStateFreerun},
				{event.GNSS, &event.GNSSData{GPSStatus: 3}, event.ClockStateLocked},
				{event.GNSS, &event.GNSSData{GPSStatus: 3, SourceLost: true}, event.ClockStateFreerun},
				{event.GNSS, &event.GNSSData{GPSStatus: 3}, event.ClockStateLocked},
				{event.DPLL, &event.DPLLData{State: event.PTP_LOCKED, LeadingSource: true}, event.ClockStateLocked},
				{event.DPLL, &event.DPLLData{State: event.PTP_HOLDOVER}, event.ClockStateHoldover},
				{event.DPLL, &event.DPLLData{State: event.PTP_FREERUN}, event.ClockStateFreerun},
				{event.DPLL, &event.DPLLData{State: event.PTP_LOCKED}, event.ClockStateLocked},
			}
			for i, tt := range tests {
				cm.ProcessEvent(event.Event{Source: tt.source, CfgName: testClockmgrPtp4lConfig,
					ClockType: clockType, IFace: iface, Time: int64(i + 1), Data: tt.data})
				labels[processLabel] = string(tt.source)
				requireGaugeValue(t, gauge, labels, tt.want)
			}

			// An older sample must not overwrite the accepted state.
			cm.ProcessEvent(event.Event{Source: event.DPLL, CfgName: testClockmgrPtp4lConfig,
				ClockType: clockType, IFace: iface, Time: 1, Data: &event.DPLLData{State: event.PTP_FREERUN}})
			requireGaugeValue(t, gauge, labels, event.ClockStateLocked)

			// DPLL ports have independent state, not the worst-of-process state.
			cm.ProcessEvent(event.Event{Source: event.DPLL, CfgName: testClockmgrPtp4lConfig,
				ClockType: clockType, IFace: "other-port", Time: 10, Data: &event.DPLLData{State: event.PTP_FREERUN}})
			requireGaugeValue(t, gauge, labels, event.ClockStateLocked)
			otherLabels := prometheus.Labels{processLabel: "dpll", nodeLabel: "testnode", ifaceLabel: "other-port"}
			requireGaugeValue(t, gauge, otherLabels, event.ClockStateFreerun)

			// Reset removes the restored process series without deleting GNSS.
			cm.ProcessEvent(event.Event{Source: event.DPLL, CfgName: testClockmgrPtp4lConfig, Reset: true})
			require.False(t, gauge.Delete(labels))
			require.False(t, gauge.Delete(otherLabels))
			labels[processLabel] = "gnss"
			requireGaugeValue(t, gauge, labels, event.ClockStateLocked)
		})
	}
}

func TestPTPSourceClockStateMetricsFollowSelectedPort(t *testing.T) {
	for _, tc := range []struct {
		name    string
		samples int
		state   float64
	}{
		{"qualified", 2, event.ClockStateLocked},
		{"qualifying", 1, event.ClockStateFreerun},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cm, gauge := newProcessMetricManager(t, event.TBC)
			alias.SetAlias("tr0", "tr0-alias")
			alias.SetAlias("tr1", "tr1-alias")
			t.Cleanup(alias.ClearAliases)
			generation, err := cm.ConfigureSourceQualification(testClockmgrPtp4lConfig, []string{"tr0", "tr1"}, utils.QualificationConfig{
				QualifiedThreshold: 100, DisqualifiedThreshold: 200, QualifiedSamples: 2, DisqualifiedSamples: 1})
			require.NoError(t, err)
			observe := func(iface string, observation event.PTPSourceObservation) event.SourceQualification {
				observation.Generation = generation
				observation.Reply = make(chan event.SourceQualification, 1)
				cm.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: testClockmgrPtp4lConfig, IFace: iface, Data: &observation})
				return <-observation.Reply
			}
			oldLabels := prometheus.Labels{processLabel: "ptp4l", nodeLabel: "testnode", ifaceLabel: "tr0-alias"}
			newLabels := prometheus.Labels{processLabel: "ptp4l", nodeLabel: "testnode", ifaceLabel: "tr1-alias"}
			// Reselection must retire only this source's clock-state series,
			// not another process's state or the previous port's raw offset.
			cm.ProcessEvent(event.Event{Source: event.DPLL, CfgName: testClockmgrPtp4lConfig, IFace: "tr0",
				Data: &event.DPLLData{State: event.PTP_LOCKED}})
			cm.offsetMetric = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_ptp_source_offset_ns"},
				[]string{fromLabel, processLabel, nodeLabel, ifaceLabel})
			cm.updateMetrics(event.Event{Source: event.PTP4l, CfgName: testClockmgrPtp4lConfig, IFace: "tr0", Data: &event.OffsetData{Offset: 17}})
			observe("", event.PTPSourceObservation{Selection: parserconstants.BMCAExternal})
			observe("tr0", event.PTPSourceObservation{PortRole: utils.ToPtr(parserconstants.PortRoleSlave)})
			for i := 0; i < tc.samples; i++ {
				observe("tr0", event.PTPSourceObservation{ServoState: "s2", Offset: 10})
			}
			requireGaugeValue(t, gauge, oldLabels, tc.state)

			// Ignored roles and stale publishers must not move the metric away
			// from the accepted selected port.
			observe("tr1", event.PTPSourceObservation{PortRole: utils.ToPtr(parserconstants.PortRoleMaster)})
			observe("not-upstream", event.PTPSourceObservation{PortRole: utils.ToPtr(parserconstants.PortRoleSlave)})
			stale := &event.PTPSourceObservation{Generation: generation + 1, PortRole: utils.ToPtr(parserconstants.PortRoleSlave), Reply: make(chan event.SourceQualification, 1)}
			cm.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: testClockmgrPtp4lConfig, IFace: "tr1", Data: stale})
			require.Empty(t, (<-stale.Reply).SourceType)
			beforeSource, ok := cm.SourceQualification(testClockmgrPtp4lConfig)
			require.True(t, ok)
			beforeState := cm.GetClock(testClockmgrPtp4lConfig).(*clock.TBC).GetSyncState()
			cm.SetApplying(true)
			result := observe("tr1", event.PTPSourceObservation{PortRole: utils.ToPtr(parserconstants.PortRoleSlave)})
			require.Empty(t, result.SourceType, "a label-only change must not trigger an input hardware action")
			requireGaugeValue(t, gauge, oldLabels, tc.state)
			require.False(t, gauge.Delete(newLabels))
			cm.SetApplying(false)
			require.False(t, gauge.Delete(oldLabels), "the old port's state series must be removed without waiting for another sample")
			require.NotContains(t, cm.metricCache, metricKey{cfgName: testClockmgrPtp4lConfig, process: "ptp4l", iface: "tr0-alias", dataType: "clock_state"})
			requireGaugeValue(t, gauge, newLabels, tc.state)
			afterSource, ok := cm.SourceQualification(testClockmgrPtp4lConfig)
			require.True(t, ok)
			beforeSource.IFace = "tr1"
			require.Equal(t, beforeSource, afterSource, "port reselection must preserve qualification and its generation/revision")
			require.Equal(t, beforeState, cm.GetClock(testClockmgrPtp4lConfig).(*clock.TBC).GetSyncState(), "a label-only change must not evaluate aggregate clock state")
			observe("tr1", event.PTPSourceObservation{ServoState: "s2", Offset: 10})
			requireGaugeValue(t, gauge, newLabels, event.ClockStateLocked)
			observe("", event.PTPSourceObservation{Selection: parserconstants.BMCALocal})
			requireGaugeValue(t, gauge, newLabels, event.ClockStateFreerun)
			require.False(t, gauge.Delete(oldLabels), "later BMCA loss must not leave an old LOCKED series behind")
			requireGaugeValue(t, gauge, prometheus.Labels{processLabel: "dpll", nodeLabel: "testnode", ifaceLabel: "tr0-alias"}, event.ClockStateLocked)
			requireGaugeValue(t, cm.offsetMetric, prometheus.Labels{fromLabel: "ptp4l", processLabel: "ptp4l", nodeLabel: "testnode", ifaceLabel: "tr0-alias"}, 17)
			observe("tr0", event.PTPSourceObservation{PortRole: utils.ToPtr(parserconstants.PortRoleSlave)})
			requireGaugeValue(t, gauge, oldLabels, event.ClockStateFreerun)
			require.False(t, gauge.Delete(newLabels), "reselecting the original port must retire the backup port's series too")
		})
	}
}

func TestSourcePortRolesCoalesceBetweenBMCASelections(t *testing.T) {
	cm, _ := newProcessMetricManager(t, event.TBC)
	generation, err := cm.ConfigureSourceQualification(testClockmgrPtp4lConfig, []string{"tr0", "tr1"}, utils.QualificationConfig{
		QualifiedThreshold: 100, DisqualifiedThreshold: 200, QualifiedSamples: 1, DisqualifiedSamples: 1})
	require.NoError(t, err)
	role := func(iface string) {
		observation := &event.PTPSourceObservation{Generation: generation, PortRole: utils.ToPtr(parserconstants.PortRoleSlave), Reply: make(chan event.SourceQualification, 1)}
		cm.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: testClockmgrPtp4lConfig, IFace: iface, Data: observation})
		require.Len(t, observation.Reply, 1)
		require.Empty(t, (<-observation.Reply).SourceType)
	}
	cm.SetApplying(true)
	for i := 0; i < 100; i++ {
		role("tr0")
		role("not-upstream")
		role("tr1")
	}
	role("not-upstream")
	require.Len(t, cm.pendingSourceObservations, 3, "repeated roles must not grow the apply queue")
	selection := &event.PTPSourceObservation{Generation: generation, Selection: parserconstants.BMCAExternal, Reply: make(chan event.SourceQualification, 1)}
	cm.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: testClockmgrPtp4lConfig, Data: selection})
	for i := 0; i < 100; i++ {
		role("tr1")
		role("not-upstream")
		role("tr0")
	}
	role("not-upstream")
	require.Len(t, cm.pendingSourceObservations, 7, "roles on either side of BMCA must remain ordered")
	cm.SetApplying(false)
	require.Len(t, selection.Reply, 1)
	require.Equal(t, "tr1", (<-selection.Reply).IFace)
	source, exists := cm.SourceQualification(testClockmgrPtp4lConfig)
	require.True(t, exists)
	require.Equal(t, "tr0", source.IFace)
}

func TestSyncEClockStateMetrics(t *testing.T) {
	cm, gauge := newProcessMetricManager(t, event.TBC)
	const cfgName = "synce4l.0.config"
	const iface = "synce-metric-port"
	alias.SetAlias(iface, "synce-metric-alias")
	t.Cleanup(alias.ClearAliases)
	labels := prometheus.Labels{processLabel: string(event.SYNCE), nodeLabel: "testnode", ifaceLabel: "synce-metric-alias"}
	send := func(data *event.SyncEData) {
		cm.ProcessEvent(event.Event{Source: event.SYNCE, CfgName: cfgName, IFace: iface, Data: data})
	}
	// Quality can be reported before the first EEC state, without implying lock.
	send(&event.SyncEData{State: event.PTP_UNKNOWN})
	require.False(t, gauge.Delete(labels))
	before := cm.GetClock(testClockmgrPtp4lConfig).GetState()
	for _, tt := range []struct {
		state event.PTPState
		want  float64
	}{
		{event.PTP_LOCKED, event.ClockStateLocked},
		{event.PTP_HOLDOVER, event.ClockStateHoldover},
		{event.PTP_FREERUN, event.ClockStateFreerun},
		{event.PTP_LOCKED, event.ClockStateLocked},
	} {
		send(&event.SyncEData{State: tt.state, EECState: "test EEC state"})
		requireGaugeValue(t, gauge, labels, tt.want)
	}
	send(&event.SyncEData{State: event.PTP_UNKNOWN})
	requireGaugeValue(t, gauge, labels, event.ClockStateLocked)
	require.Equal(t, before, cm.GetClock(testClockmgrPtp4lConfig).GetState(), "SyncE must not change aggregate PTP state")
	cm.ProcessEvent(event.Event{Source: event.SYNCE, CfgName: cfgName, Reset: true})
	require.False(t, gauge.Delete(labels), "reset must remove SyncE state series")
}

func TestProcessClockStateMetricsWithoutInterface(t *testing.T) {
	for _, iface := range []string{"", event.LEADING_INTERFACE_UNKNOWN} {
		t.Run(iface, func(t *testing.T) {
			cm, gauge := newProcessMetricManager(t, event.GM)
			cm.ProcessEvent(event.Event{Source: event.GNSS, CfgName: testClockmgrPtp4lConfig,
				ClockType: event.GM, IFace: iface, Data: &event.GNSSData{GPSStatus: 3}})
			require.False(t, gauge.Delete(prometheus.Labels{
				processLabel: "gnss", nodeLabel: "testnode", ifaceLabel: iface,
			}))
		})
	}
}
