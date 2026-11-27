package clockmgr_test

import (
	"sync"
	"testing"
	"testing/synctest"
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
	// Source readiness is retained in snapshots but does not imply aggregate lock.
	m := clockmgr.Init("test", make(chan event.Event), nil, nil, nil, nil)
	set := qualificationPublisher(t, m)
	set(true)
	stats := receiveStatsSnapshot(t, m.StatsUpdatesCh())
	require.True(t, stats.SourceQualified["ptp4l.0.config"], "latest state retained even without a waiting consumer")
	require.NotEqual(t, event.PTP_LOCKED, stats.ClockStates["ptp4l.0.config"], "source qualification must not fabricate aggregate lock")
	source, ok := m.SourceQualification("ptp4l.0.config")
	require.True(t, ok)
	require.Equal(t, event.PTP4l, source.SourceType)
	require.Equal(t, clock.PTPPortSource{Port: "tr0"}, source.Source)
	require.Equal(t, "tr0", source.IFace)
	require.Empty(t, clockmgr.SnapshotStatsForTest(m, []process.StatsRequest{{ClockID: "ptp4l.0.config", Source: event.TS2PHC}}).SourceQualified)
	set(false)
	require.False(t, receiveStatsSnapshot(t, m.StatsUpdatesCh()).SourceQualified["ptp4l.0.config"])
	require.True(t, stats.SourceQualified["ptp4l.0.config"], "snapshot is detached")
	// A replacement, reset, or removal must invalidate the old publisher.
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
			m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", IFace: "tr0", Data: &event.PTPSourceObservation{
				Generation: generation, PortRole: utils.ToPtr(parserconstants.PortRoleSlave)}})
		}
		m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", IFace: "tr0", Data: &event.PTPSourceObservation{
			Generation: generation, ServoState: "s2", Offset: 10, SourceLost: !qualified}})
	}
}

func TestSourceQualificationEventRoundTrip(t *testing.T) {
	// Queued observations are not readiness until the clock evaluates them;
	// an averaged offset must not be counted as a fresh qualification sample.
	events := make(chan event.Event, 2)
	m := clockmgr.Init("test", events, nil, nil, nil, nil)
	qualificationPublisher(t, m)
	generation, err := m.ConfigureSourceQualification("ptp4l.0.config", []string{"tr0"}, utils.QualificationConfig{
		QualifiedThreshold: 100, DisqualifiedThreshold: 200, QualifiedSamples: 2, DisqualifiedSamples: 1})
	require.NoError(t, err)
	role := &event.PTPSourceObservation{Generation: generation, PortRole: utils.ToPtr(parserconstants.PortRoleSlave)}
	sample := &event.PTPSourceObservation{Generation: generation, ServoState: "s2", Offset: 10}
	wrap := func(data event.EventData) event.Event {
		return event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", IFace: "tr0", Data: data}
	}
	events <- wrap(role)
	events <- wrap(sample)
	require.False(t, clockmgr.SnapshotStatsForTest(m, []process.StatsRequest{{ClockID: "ptp4l.0.config"}}).SourceQualified["ptp4l.0.config"], "queued observations are not evaluated readiness")
	m.ProcessEvent(<-events)
	selected, exists := m.SourceQualification("ptp4l.0.config")
	require.True(t, exists)
	require.Equal(t, "tr0", selected.IFace)
	m.ProcessEvent(<-events)
	result, exists := m.SourceQualification("ptp4l.0.config")
	require.True(t, exists)
	require.False(t, result.Qualified)
	require.Equal(t, clock.PTPPortSource{Port: "tr0"}, result.Source)
	m.ProcessEvent(wrap(&event.OffsetData{State: event.PTP_LOCKED, Offset: 10}))
	require.False(t, clockmgr.SnapshotStatsForTest(m, []process.StatsRequest{{ClockID: "ptp4l.0.config"}}).SourceQualified["ptp4l.0.config"], "averaged offsets must not count as fresh qualification samples")
	m.ProcessEvent(wrap(sample))
	result, exists = m.SourceQualification("ptp4l.0.config")
	require.True(t, exists && result.Qualified)
	m.SetApplying(true)
	m.ProcessEvent(wrap(sample))
	require.False(t, receiveStatsSnapshot(t, m.StatsUpdatesCh()).SourceQualified["ptp4l.0.config"], "ordinary samples are suppressed during apply")
	m.SetApplying(false)
	m.RemoveAllClocks()
	m.ProcessEvent(wrap(sample))
	_, exists = m.SourceQualification("ptp4l.0.config")
	require.False(t, exists, "removed clocks must ignore their old scanner")
}

func TestSourceTransitionsRetainedDuringApply(t *testing.T) {
	// Apply retains port roles and losses in order, but suppresses servo samples.
	m := clockmgr.Init("test", make(chan event.Event), nil, nil, nil, nil)
	qualificationPublisher(t, m)
	generation, err := m.ConfigureSourceQualification("ptp4l.0.config", []string{"tr0", "tr1"}, utils.QualificationConfig{
		QualifiedThreshold: 100, DisqualifiedThreshold: 200, QualifiedSamples: 1, DisqualifiedSamples: 1})
	require.NoError(t, err)
	send := func(iface string, observation *event.PTPSourceObservation) {
		m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", IFace: iface, Data: observation})
	}
	role := &event.PTPSourceObservation{Generation: generation, PortRole: utils.ToPtr(parserconstants.PortRoleSlave)}
	sample := &event.PTPSourceObservation{Generation: generation, ServoState: "s2", Offset: 10}
	m.SetApplying(true)
	send("tr0", role)
	// Reusing the original payload must not alter the retained copy.
	*role.PortRole = parserconstants.PortRoleMaster
	send("tr0", sample)
	m.SetApplying(false)
	result, exists := m.SourceQualification("ptp4l.0.config")
	require.True(t, exists)
	require.Equal(t, "tr0", result.IFace, "the retained role must be copied")
	require.False(t, result.Qualified, "samples during apply must not count")
	send("tr0", sample)
	result, _ = m.SourceQualification("ptp4l.0.config")
	require.True(t, result.Qualified)

	// A loss after selecting tr1 must be replayed after that selection.
	m.SetApplying(true)
	send("tr0", &event.PTPSourceObservation{Generation: generation, PortRole: utils.ToPtr(parserconstants.PortRoleMaster)})
	send("tr1", &event.PTPSourceObservation{Generation: generation, PortRole: utils.ToPtr(parserconstants.PortRoleSlave)})
	send("", &event.PTPSourceObservation{Generation: generation, SourceLost: true})
	m.SetApplying(false)
	send("tr1", sample)
	result, _ = m.SourceQualification("ptp4l.0.config")
	require.Equal(t, "tr1", result.IFace)
	require.False(t, result.Qualified, "later source loss must invalidate the retained selection")
	send("tr1", &event.PTPSourceObservation{Generation: generation, PortRole: utils.ToPtr(parserconstants.PortRoleSlave)})
	send("tr1", sample)
	result, _ = m.SourceQualification("ptp4l.0.config")
	require.True(t, result.Qualified)
}

func TestRetainedSourceTransitionRejectsOldGeneration(t *testing.T) {
	// Reconfigure before replay: the old port selection must not qualify the new source.
	m := clockmgr.Init("test", make(chan event.Event), nil, nil, nil, nil)
	qualificationPublisher(t, m)
	old, exists := m.SourceQualification("ptp4l.0.config")
	require.True(t, exists)
	m.SetApplying(true)
	m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", IFace: "tr0",
		Data: &event.PTPSourceObservation{Generation: old.Generation, PortRole: utils.ToPtr(parserconstants.PortRoleSlave)}})
	generation, err := m.ConfigureSourceQualification("ptp4l.0.config", []string{"tr0"}, utils.QualificationConfig{
		QualifiedThreshold: 100, DisqualifiedThreshold: 200, QualifiedSamples: 1, DisqualifiedSamples: 1})
	require.NoError(t, err)
	m.SetApplying(false)
	m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", IFace: "tr0",
		Data: &event.PTPSourceObservation{Generation: generation, ServoState: "s2", Offset: 10}})
	result, exists := m.SourceQualification("ptp4l.0.config")
	require.True(t, exists)
	require.Empty(t, result.IFace)
	require.False(t, result.Qualified, "an obsolete selected port must not establish availability")
}

func TestSourceInputActionTeardownDoesNotHoldManagerLock(t *testing.T) {
	// The blocked hardware callback queries the manager; holding its lock while
	// waiting for the callback would deadlock both removal and replacement.
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "remove", true: "replace"}[replace], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m := clockmgr.Init("test", make(chan event.Event), nil, nil, nil, nil)
				qualificationPublisher(t, m)
				source, exists := m.SourceQualification("ptp4l.0.config")
				require.True(t, exists)
				started := make(chan struct{})
				release := make(chan struct{})
				done := make(chan error, 1)
				var releaseOnce sync.Once
				finish := func() { releaseOnce.Do(func() { close(release) }) }
				t.Cleanup(finish)
				_, err := m.SetSourceInputAction("ptp4l.0.config", source.Generation, "test", func(bool) error {
					close(started)
					<-release
					m.SourceQualification("ptp4l.0.config")
					return nil
				})
				require.NoError(t, err)
				m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", IFace: "tr0",
					Data: &event.PTPSourceObservation{Generation: source.Generation, PortRole: utils.ToPtr(parserconstants.PortRoleSlave)}})
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("input action did not start")
				}
				go func() {
					if replace {
						clk, replaceErr := clock.NewTBC("ptp4l.0.config", m.GetUtcOffset, &pmc.MockClient{}, "ptp4.1.config")
						if replaceErr == nil {
							replaceErr = m.AddClock(clk)
						}
						done <- replaceErr
					} else {
						m.RemoveAllClocks()
						done <- nil
					}
				}()
				// Let teardown reach its wait before allowing the callback to query the manager.
				synctest.Wait()
				finish()
				select {
				case teardownErr := <-done:
					require.NoError(t, teardownErr)
				case <-time.After(time.Second):
					t.Fatal("clock teardown deadlocked with the input action")
				}
			})
		})
	}
}
