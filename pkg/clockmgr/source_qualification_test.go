package clockmgr_test

import (
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clockmgr"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	parserconstants "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/stretchr/testify/require"
)

func TestSourceQualificationSnapshots(t *testing.T) {
	// Reset/remove touch shared debug state, so keep this test serial.
	m := clockmgr.Init("test", make(chan event.Event), nil, nil, nil, nil)
	set := qualificationPublisher(t, m)
	set(true)
	stats := receiveStatsSnapshot(t, m.StatsUpdatesCh())
	require.True(t, stats.SourceQualified["ptp4l.0.config"], "latest state retained even without a waiting consumer")
	require.NotEqual(t, event.PTP_LOCKED, stats.ClockStates["ptp4l.0.config"], "source qualification must not fabricate aggregate lock")
	source, ok := m.SourceQualification("ptp4l.0.config")
	require.True(t, ok)
	require.Equal(t, event.PTP4l, source.SourceType)
	require.Equal(t, clock.PTPInstanceSource{ConfigName: "ptp4l.0.config", Generation: source.Generation}, source.Source)
	require.Equal(t, "tr0", source.IFace)
	require.Empty(t, clockmgr.SnapshotStatsForTest(m, []process.StatsRequest{{ClockID: "ptp4l.0.config", Source: event.TS2PHC}}).SourceQualified)
	set(false)
	require.False(t, receiveStatsSnapshot(t, m.StatsUpdatesCh()).SourceQualified["ptp4l.0.config"])
	require.True(t, stats.SourceQualified["ptp4l.0.config"], "snapshot is detached")
	newSet := qualificationPublisher(t, m)
	require.False(t, receiveStatsSnapshot(t, m.StatsUpdatesCh()).SourceQualified["ptp4l.0.config"])
	set(true)
	require.False(t, receiveStatsSnapshot(t, m.StatsUpdatesCh()).SourceQualified["ptp4l.0.config"], "superseded owner cannot republish readiness")
	newSet(true)
	require.True(t, receiveStatsSnapshot(t, m.StatsUpdatesCh()).SourceQualified["ptp4l.0.config"])
	m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", Reset: true})
	require.False(t, receiveStatsSnapshot(t, m.StatsUpdatesCh()).SourceQualified["ptp4l.0.config"])
	newSet(true)
	require.False(t, receiveStatsSnapshot(t, m.StatsUpdatesCh()).SourceQualified["ptp4l.0.config"], "reset revokes old publisher")
	set = qualificationPublisher(t, m)
	set(true)
	m.RemoveAllClocks()
	set(true)
	require.Empty(t, receiveStatsSnapshot(t, m.StatsUpdatesCh()).SourceQualified, "removed owner cannot resurrect readiness")
	require.Empty(t, m.StatsUpdatesCh(), "removed owner cannot publish another snapshot")
}

func qualificationPublisher(t *testing.T, m *clockmgr.ClockManager) func(bool) {
	t.Helper()
	clk, err := clock.NewTBC("ptp4l.0.config", m.GetUtcOffset, &pmc.MockClient{}, "ptp4.1.config")
	require.NoError(t, err)
	require.NoError(t, m.AddClock(clk))
	generation, err := m.ConfigureSourceQualification("ptp4l.0.config", []string{"tr0"}, utils.QualificationConfig{
		QualifiedThreshold: 100, DisqualifiedThreshold: 200, QualifiedSamples: 1, DisqualifiedSamples: 1})
	require.NoError(t, err)
	return func(qualified bool) {
		if qualified {
			m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", Data: &event.PTPSourceObservation{
				Generation: generation, Selection: parserconstants.BMCAExternal}})
			m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", IFace: "tr0", Data: &event.PTPSourceObservation{
				Generation: generation, PortRole: utils.ToPtr(parserconstants.PortRoleSlave)}})
		}
		m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", IFace: "tr0", Data: &event.PTPSourceObservation{
			Generation: generation, ServoState: "s2", Offset: 10, SourceLost: !qualified}})
	}
}

func TestSourceQualificationEventRoundTrip(t *testing.T) {
	events := make(chan event.Event, 2)
	m := clockmgr.Init("test", events, nil, nil, nil, nil)
	qualificationPublisher(t, m)
	generation, err := m.ConfigureSourceQualification("ptp4l.0.config", []string{"tr0"}, utils.QualificationConfig{
		QualifiedThreshold: 100, DisqualifiedThreshold: 200, QualifiedSamples: 2, DisqualifiedSamples: 1})
	require.NoError(t, err)
	role := &event.PTPSourceObservation{Generation: generation, PortRole: utils.ToPtr(parserconstants.PortRoleSlave), Reply: make(chan event.SourceQualification, 1)}
	sample := &event.PTPSourceObservation{Generation: generation, ServoState: "s2", Offset: 10, Reply: make(chan event.SourceQualification, 1)}
	wrap := func(data event.EventData) event.Event {
		return event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", IFace: "tr0", Data: data}
	}
	events <- wrap(role)
	events <- wrap(sample)
	require.False(t, clockmgr.SnapshotStatsForTest(m, []process.StatsRequest{{ClockID: "ptp4l.0.config"}}).SourceQualified["ptp4l.0.config"], "queued observations are not evaluated readiness")
	m.ProcessEvent(<-events)
	require.Empty(t, (<-role.Reply).SourceType, "role changes do not evaluate qualification")
	selected, exists := m.SourceQualification("ptp4l.0.config")
	require.True(t, exists)
	require.Equal(t, "tr0", selected.IFace)
	require.False(t, selected.Qualified)
	m.ProcessEvent(wrap(&event.PTPSourceObservation{Generation: generation, Selection: parserconstants.BMCAExternal}))
	m.ProcessEvent(<-events)
	result := <-sample.Reply
	require.False(t, result.Qualified)
	require.Equal(t, clock.PTPInstanceSource{ConfigName: "ptp4l.0.config", Generation: generation}, result.Source)
	m.ProcessEvent(wrap(&event.OffsetData{State: event.PTP_LOCKED, Offset: 10}))
	require.False(t, clockmgr.SnapshotStatsForTest(m, []process.StatsRequest{{ClockID: "ptp4l.0.config"}}).SourceQualified["ptp4l.0.config"], "averaged offsets must not count as fresh qualification samples")
	m.ProcessEvent(wrap(sample))
	require.True(t, (<-sample.Reply).Qualified)
	m.SetApplying(true)
	m.ProcessEvent(wrap(sample))
	require.Empty(t, (<-sample.Reply).SourceType, "suppressed input still gets a rejection reply")
	m.SetApplying(false)
	m.RemoveAllClocks()
	m.ProcessEvent(wrap(sample))
	require.Empty(t, (<-sample.Reply).SourceType, "removed clocks must not strand their old scanner")
}

func receiveQualificationReply(t *testing.T, replies <-chan event.SourceQualification) event.SourceQualification {
	t.Helper()
	select {
	case result := <-replies:
		return result
	case <-time.After(time.Second):
		t.Fatal("source observation did not receive a reply")
		return event.SourceQualification{}
	}
}

func TestSourceQualificationBMCAWaitsForApply(t *testing.T) {
	for _, selection := range []parserconstants.BMCASelection{parserconstants.BMCAExternal, parserconstants.BMCALocal} {
		t.Run(string(selection), func(t *testing.T) {
			m := clockmgr.Init("test", make(chan event.Event), nil, nil, nil, nil)
			qualificationPublisher(t, m)
			source, ok := m.SourceQualification("ptp4l.0.config")
			require.True(t, ok)
			observation := &event.PTPSourceObservation{Generation: source.Generation, Selection: selection, Reply: make(chan event.SourceQualification, 1)}
			wrap := func(data event.EventData) event.Event {
				return event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", IFace: "tr0", Data: data}
			}

			m.SetApplying(true)
			m.ProcessEvent(wrap(observation))
			require.Empty(t, observation.Reply, "a one-shot BMCA selection must wait for evaluation, not receive a rejection")
			require.Equal(t, event.PTP_NOTSET, receiveStatsSnapshot(t, m.StatsUpdatesCh()).ClockStates["ptp4l.0.config"], "retention must not evaluate the clock during apply")
			sample := &event.PTPSourceObservation{Generation: source.Generation, ServoState: "s2", Offset: 10, Reply: make(chan event.SourceQualification, 1)}
			m.ProcessEvent(wrap(sample))
			require.Empty(t, receiveQualificationReply(t, sample.Reply).SourceType, "ordinary samples are still suppressed")

			m.SetApplying(false)
			result := receiveQualificationReply(t, observation.Reply)
			require.Equal(t, event.PTP4l, result.SourceType)
			require.Equal(t, source.Generation, result.Generation)
			require.False(t, result.Ready, "suppressed samples must not count towards qualification")
			m.ProcessEvent(wrap(sample))
			require.Equal(t, selection == parserconstants.BMCAExternal, receiveQualificationReply(t, sample.Reply).Ready, "fresh samples must use the retained selection without another BMCA event")
			m.SetApplying(false)
			require.Empty(t, observation.Reply, "the retained observation must receive exactly one reply")
		})
	}
}

func TestSourceQualificationPortSelectionReplayedAfterApply(t *testing.T) {
	m := clockmgr.Init("test", make(chan event.Event), nil, nil, nil, nil)
	qualificationPublisher(t, m)
	_, err := m.ConfigureSourceQualification("ptp4l.0.config", []string{"tr0", "tr1"}, utils.QualificationConfig{
		QualifiedThreshold: 100, DisqualifiedThreshold: 200, QualifiedSamples: 1, DisqualifiedSamples: 1})
	require.NoError(t, err)
	source, exists := m.SourceQualification("ptp4l.0.config")
	require.True(t, exists)
	role := &event.PTPSourceObservation{Generation: source.Generation, PortRole: utils.ToPtr(parserconstants.PortRoleSlave), Reply: make(chan event.SourceQualification, 1)}
	selection := &event.PTPSourceObservation{Generation: source.Generation, Selection: parserconstants.BMCAExternal, Reply: make(chan event.SourceQualification, 1)}
	m.SetApplying(true)
	m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", IFace: "tr0", Data: role})
	require.Empty(t, receiveQualificationReply(t, role.Reply).SourceType, "port metadata must not block the scanner")
	*role.PortRole = parserconstants.PortRoleMaster // Reusing the acknowledged observation must not alter the replay.
	before, _ := m.SourceQualification("ptp4l.0.config")
	require.Empty(t, before.IFace, "the clock must not change during apply")
	m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", Data: selection})
	require.Empty(t, selection.Reply)
	laterRole := &event.PTPSourceObservation{Generation: source.Generation, PortRole: utils.ToPtr(parserconstants.PortRoleSlave), Reply: make(chan event.SourceQualification, 1)}
	m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", IFace: "tr1", Data: laterRole})
	require.Empty(t, receiveQualificationReply(t, laterRole.Reply).SourceType)
	m.SetApplying(false)
	require.Equal(t, "tr0", receiveQualificationReply(t, selection.Reply).IFace, "port selection must be replayed before BMCA")
	after, _ := m.SourceQualification("ptp4l.0.config")
	require.Equal(t, "tr1", after.IFace, "later port selections must retain their order")
	require.Empty(t, role.Reply, "replaying an acknowledged port role must not send another reply")
	require.Empty(t, laterRole.Reply)

	m.SetApplying(true)
	obsolete := &event.PTPSourceObservation{Generation: source.Generation, PortRole: utils.ToPtr(parserconstants.PortRoleSlave), Reply: make(chan event.SourceQualification, 1)}
	m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", IFace: "tr0", Data: obsolete})
	require.Empty(t, receiveQualificationReply(t, obsolete.Reply).SourceType)
	m.RemoveAllClocks()
	qualificationPublisher(t, m)
	m.SetApplying(false)
	require.Empty(t, obsolete.Reply, "an obsolete port observation must not be replayed")
	replacement, exists := m.SourceQualification("ptp4l.0.config")
	require.True(t, exists)
	require.Empty(t, replacement.IFace, "an obsolete port must not affect a replacement clock")
}

func TestSourceQualificationBMCAApplyOrder(t *testing.T) {
	m := clockmgr.Init("test", make(chan event.Event), nil, nil, nil, nil)
	set := qualificationPublisher(t, m)
	set(true)
	source, ok := m.SourceQualification("ptp4l.0.config")
	require.True(t, ok)
	require.True(t, source.Ready)
	m.SetApplying(true)
	var observations []*event.PTPSourceObservation
	for _, selection := range []parserconstants.BMCASelection{parserconstants.BMCALocal, parserconstants.BMCAExternal} {
		observation := &event.PTPSourceObservation{Generation: source.Generation, Selection: selection, Reply: make(chan event.SourceQualification, 1)}
		observations = append(observations, observation)
		m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", Data: observation})
		require.Empty(t, observation.Reply)
	}
	m.SetApplying(false)
	for _, observation := range observations {
		result := receiveQualificationReply(t, observation.Reply)
		require.Equal(t, event.PTP4l, result.SourceType)
		require.False(t, result.Qualified, "the local selection must reset qualification before the subsequent external selection")
	}
	sample := &event.PTPSourceObservation{Generation: source.Generation, ServoState: "s2", Offset: 10, Reply: make(chan event.SourceQualification, 1)}
	m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", IFace: "tr0", Data: sample})
	require.True(t, receiveQualificationReply(t, sample.Reply).Ready, "the final external selection must be retained in arrival order")
}

func TestSourceQualificationPendingBMCAInvalidation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		invalidate func(*testing.T, *clockmgr.ClockManager)
	}{
		{"removed", func(_ *testing.T, m *clockmgr.ClockManager) { m.RemoveAllClocks() }},
		{"replaced", func(t *testing.T, m *clockmgr.ClockManager) { qualificationPublisher(t, m) }},
		{"reconfigured", func(t *testing.T, m *clockmgr.ClockManager) {
			_, err := m.ConfigureSourceQualification("ptp4l.0.config", []string{"tr0"}, utils.QualificationConfig{
				QualifiedThreshold: 100, DisqualifiedThreshold: 200, QualifiedSamples: 1, DisqualifiedSamples: 1})
			require.NoError(t, err)
		}},
		{"invalid reconfiguration", func(t *testing.T, m *clockmgr.ClockManager) {
			_, err := m.ConfigureSourceQualification("ptp4l.0.config", nil, utils.QualificationConfig{})
			require.Error(t, err)
		}},
		{"PTP reset", func(_ *testing.T, m *clockmgr.ClockManager) {
			m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", Reset: true})
		}},
		{"all-source reset", func(_ *testing.T, m *clockmgr.ClockManager) {
			m.ProcessEvent(event.Event{CfgName: "ptp4l.0.config", Reset: true})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := clockmgr.Init("test", make(chan event.Event), nil, nil, nil, nil)
			qualificationPublisher(t, m)
			source, ok := m.SourceQualification("ptp4l.0.config")
			require.True(t, ok)
			observation := &event.PTPSourceObservation{Generation: source.Generation, Selection: parserconstants.BMCAExternal, Reply: make(chan event.SourceQualification, 1)}
			ev := event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", Data: observation}
			m.SetApplying(true)
			m.ProcessEvent(ev)
			require.Empty(t, observation.Reply)
			tc.invalidate(t, m)
			require.Empty(t, receiveQualificationReply(t, observation.Reply).SourceType, "invalidating a pending selection must release its old scanner during apply")
			qualificationPublisher(t, m)
			m.ProcessEvent(ev)
			require.Empty(t, receiveQualificationReply(t, observation.Reply).SourceType, "late observations from an obsolete generation must be rejected immediately")
			m.SetApplying(false)
			require.Empty(t, observation.Reply, "invalidated observations must not be replayed")
			source, ok = m.SourceQualification("ptp4l.0.config")
			require.True(t, ok)
			sample := &event.PTPSourceObservation{Generation: source.Generation, ServoState: "s2", Offset: 10, Reply: make(chan event.SourceQualification, 1)}
			m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", IFace: "tr0", Data: sample})
			require.False(t, receiveQualificationReply(t, sample.Reply).Ready, "an old selection must not establish availability in the replacement")
		})
	}
}

func TestSourceQualificationPendingBMCASurvivesAggregateReset(t *testing.T) {
	for _, source := range []event.EventSource{event.DPLL, event.TS2PHC} {
		t.Run(string(source), func(t *testing.T) {
			m := clockmgr.Init("test", make(chan event.Event), nil, nil, nil, nil)
			qualificationPublisher(t, m)
			qualification, ok := m.SourceQualification("ptp4l.0.config")
			require.True(t, ok)
			observation := &event.PTPSourceObservation{Generation: qualification.Generation, Selection: parserconstants.BMCAExternal, Reply: make(chan event.SourceQualification, 1)}
			m.SetApplying(true)
			m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", Data: observation})
			m.ProcessEvent(event.Event{Source: source, CfgName: "ts2phc.0.config", Reset: true})
			require.Empty(t, observation.Reply, "an aggregate-only reset must not discard a current PTP generation's selection")
			m.SetApplying(false)
			require.Equal(t, event.PTP4l, receiveQualificationReply(t, observation.Reply).SourceType)
			sample := &event.PTPSourceObservation{Generation: qualification.Generation, ServoState: "s2", Offset: 10, Reply: make(chan event.SourceQualification, 1)}
			m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", IFace: "tr0", Data: sample})
			require.True(t, receiveQualificationReply(t, sample.Reply).Ready)
		})
	}
}

func TestSourceQualificationApplyingRejectsUnrelatedObservations(t *testing.T) {
	m := clockmgr.Init("test", make(chan event.Event), nil, nil, nil, nil)
	qualificationPublisher(t, m)
	source, ok := m.SourceQualification("ptp4l.0.config")
	require.True(t, ok)
	m.SetApplying(true)
	for _, ev := range []event.Event{
		{Source: event.PTP4l, CfgName: "missing", Data: &event.PTPSourceObservation{Generation: source.Generation, Selection: parserconstants.BMCAExternal}},
		{Source: event.PTP4l, CfgName: "ts2phc.0.config", Data: &event.PTPSourceObservation{Generation: source.Generation, Selection: parserconstants.BMCAExternal}},
		{Source: event.TS2PHC, CfgName: "ptp4l.0.config", Data: &event.PTPSourceObservation{Generation: source.Generation, Selection: parserconstants.BMCAExternal}},
		{Source: event.PTP4l, CfgName: "ptp4l.0.config", Data: &event.PTPSourceObservation{Generation: source.Generation + 1, Selection: parserconstants.BMCAExternal}},
		{Source: event.PTP4l, CfgName: "ptp4l.0.config", Data: &event.PTPSourceObservation{Generation: source.Generation, PortRole: utils.ToPtr(parserconstants.PortRoleSlave)}},
		{Source: event.PTP4l, CfgName: "ptp4l.0.config", Data: &event.PTPSourceObservation{Generation: source.Generation, SourceLost: true}},
	} {
		observation := ev.Data.(*event.PTPSourceObservation)
		observation.Reply = make(chan event.SourceQualification, 1)
		m.ProcessEvent(ev)
		require.Empty(t, receiveQualificationReply(t, observation.Reply).SourceType, "unrelated observations must not produce a qualification verdict")
	}
	m.SetApplying(false)
}

func TestSourceQualificationApplyPublishesReadiness(t *testing.T) {
	m := clockmgr.Init("test", make(chan event.Event), nil, nil, nil, nil)
	set := qualificationPublisher(t, m)
	set(true)
	require.True(t, receiveStatsSnapshot(t, m.StatsUpdatesCh()).SourceQualified["ptp4l.0.config"])
	m.SetApplying(true)
	require.False(t, receiveStatsSnapshot(t, m.StatsUpdatesCh()).SourceQualified["ptp4l.0.config"])
	m.SetApplying(false)
	require.True(t, receiveStatsSnapshot(t, m.StatsUpdatesCh()).SourceQualified["ptp4l.0.config"], "apply completion must publish unsuppressed readiness even without another observation")
}
