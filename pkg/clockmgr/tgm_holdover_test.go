package clockmgr

import (
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/alias"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

const holdoverInterfaceLabel = "iface"

func TestClockOwnershipRejectsRetiredDPLLEpisode(t *testing.T) {
	for _, remove := range []bool{false, true} {
		for _, newProducer := range []bool{false, true} {
			manager := Init("test-node", make(chan event.Event, 16), nil, nil, nil, nil)
			t.Cleanup(manager.RemoveAllClocks)
			old, err := clock.NewGM("ts2phc.0.config", func() int { return 37 }, &pmc.MockClient{})
			require.NoError(t, err)
			require.NoError(t, manager.AddClock(old))
			observation := &event.HoldoverObservation{ProducerID: 1, Sequence: 1, Episode: 1,
				HardwareState: event.PTP_LOCKED, Reference: event.GNSS, MaxErrorNS: 100, TimeoutSeconds: 100}
			ev := event.Event{Source: event.DPLL, CfgName: "ts2phc.0.config", IFace: "ens2f0", Time: 1,
				Data: &event.DPLLData{State: event.PTP_LOCKED, Holdover: observation}}
			manager.ProcessEvent(ev)
			if remove {
				manager.RemoveAllClocks()
			}
			replacement, err := clock.NewGM("ts2phc.0.config", func() int { return 37 }, &pmc.MockClient{})
			require.NoError(t, err)
			require.NoError(t, manager.AddClock(replacement))
			queued := *observation
			queued.Sequence++
			ev.Data = &event.DPLLData{State: event.PTP_LOCKED, Holdover: &queued}
			manager.ProcessEvent(ev)
			for _, data := range replacement.ProcessData() {
				if data.ProcessName == event.DPLL {
					require.Empty(t, data.Details)
				}
			}
			renewed := queued
			if newProducer {
				renewed.ProducerID++
			} else {
				renewed.Episode++
			}
			ev.Data = &event.DPLLData{State: event.PTP_LOCKED, Holdover: &renewed}
			manager.ProcessEvent(ev)
			require.Len(t, replacement.GetData(event.DPLL).Details, 1)
		}
	}
}

func TestDPLLObservationReportingAndStaleMetrics(t *testing.T) {
	for _, mode := range []event.ClockType{event.GM, event.TBC} {
		offsetGauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_boundary_offset"}, []string{"from", nodeLabel, processLabel, holdoverInterfaceLabel})
		manager := Init("test-node", make(chan event.Event, 16), offsetGauge, nil, nil, nil)
		t.Cleanup(manager.RemoveAllClocks)
		var receiver clock.Clock
		var err error
		if mode == event.GM {
			receiver, err = clock.NewGM("ts2phc.0.config", func() int { return 37 }, &pmc.MockClient{})
		} else {
			receiver, err = clock.NewTBC("ts2phc.0.config", func() int { return 37 }, &pmc.MockClient{}, "")
		}
		require.NoError(t, err)
		require.NoError(t, manager.AddClock(receiver))
		data := &event.DPLLData{State: event.PTP_HOLDOVER, Offset: utils.ToPtr[int64](3), LeadingSource: true,
			Holdover: &event.HoldoverObservation{ProducerID: 1, Sequence: 2, Episode: 1, Reference: event.GNSS,
				HardwareState: event.PTP_HOLDOVER, Active: true, Acquired: true, Valid: true,
				EstimatedErrorNS: 25, PeakErrorNS: 25, MaxErrorNS: 100, InSpecErrorNS: 40, TimeoutSeconds: 100}}
		ev := event.Event{Source: event.DPLL, IFace: "ens2f0", CfgName: "ts2phc.0.config", ClockType: mode, Time: 1, Data: data}
		manager.ProcessEvent(ev)
		expected := float64(3)
		if mode == event.TBC {
			expected = 25
		}
		metric := offsetGauge.WithLabelValues(string(event.DPLL), "test-node", string(event.DPLL), alias.GetAlias(ev.IFace))
		require.Equal(t, expected, testutil.ToFloat64(metric))
		require.Equal(t, int64(3), *data.Offset)
		data.Holdover = &event.HoldoverObservation{ProducerID: 1, Sequence: 1, HardwareState: event.PTP_FREERUN}
		data.Offset = utils.ToPtr[int64](999)
		ev.Time++
		manager.ProcessEvent(ev)
		require.Equal(t, expected, testutil.ToFloat64(metric))
	}
}

func TestTGMHoldoverClassOnlyMetricsAndIPC(t *testing.T) {
	stateGauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_tgm_state"}, []string{processLabel, nodeLabel, holdoverInterfaceLabel})
	classGauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_tgm_class"}, []string{"process", "node", "config"})
	cache := ipc.NewCache(64)
	manager := Init("test-node", make(chan event.Event, 64), nil, stateGauge, classGauge, cache)
	t.Cleanup(manager.RemoveAllClocks)
	gm, err := clock.NewGM("ts2phc.0.config", func() int { return 37 }, &pmc.MockClient{})
	require.NoError(t, err)
	require.NoError(t, manager.AddClock(gm))
	base := event.Event{CfgName: "ts2phc.0.config", IFace: "ens2f0", ClockType: event.GM, Time: 1}
	base.Source, base.Data = event.GNSS, &event.GNSSData{GPSStatus: 3}
	manager.ProcessEvent(base)
	base.Source, base.Data = event.TS2PHC, &event.OffsetData{State: event.PTP_LOCKED}
	manager.ProcessEvent(base)
	for index, sample := range []struct {
		state event.PTPState
		out   bool
		class float64
		value float64
	}{
		{event.PTP_LOCKED, false, 6, 1},
		{event.PTP_HOLDOVER, false, 7, 2},
		{event.PTP_HOLDOVER, true, 140, 2},
		{event.PTP_HOLDOVER, true, 140, 2},
		{event.PTP_FREERUN, true, 248, 0},
	} {
		for len(cache.Out()) > 0 {
			<-cache.Out()
		}
		for len(manager.SyncStatusUpdateCh()) > 0 {
			<-manager.SyncStatusUpdateCh()
		}
		for len(manager.StatsUpdatesCh()) > 0 {
			<-manager.StatsUpdatesCh()
		}
		previousClass := gm.ClockClass()
		previousState := gm.GetState()
		start := time.Now()
		rawState, estimate := event.PTP_HOLDOVER, int64(10)
		if sample.state == event.PTP_LOCKED {
			rawState = event.PTP_LOCKED
		}
		if sample.out {
			estimate = 50
		}
		if sample.state == event.PTP_FREERUN {
			estimate = 101
		}
		base.Source, base.Data = event.DPLL, &event.DPLLData{State: rawState, LeadingSource: true,
			Holdover: &event.HoldoverObservation{HardwareState: rawState, ProducerID: 1, Sequence: uint64(index + 1), Episode: 1,
				Reference: event.GNSS, Acquired: true, Active: rawState == event.PTP_HOLDOVER, Valid: true,
				EstimatedErrorNS: estimate, PeakErrorNS: estimate, TimeoutSeconds: 100, MaxErrorNS: 100, InSpecErrorNS: 40}}
		manager.ProcessEvent(base)
		require.Less(t, time.Since(start), time.Second)
		require.Equal(t, sample.class, testutil.ToFloat64(classGauge.WithLabelValues("ptp4l", "test-node", "ptp4l.0.config")))
		require.Equal(t, sample.value, testutil.ToFloat64(stateGauge.WithLabelValues("GM", "test-node", alias.GetAlias("ens2f0"))))
		classEvents, lockEvents := 0, 0
		for len(cache.Out()) > 0 {
			message := <-cache.Out()
			switch message.Type {
			case ipc.TypeClockClass:
				classEvents++
				require.Equal(t, "ens2f0", message.IFace)
				require.Equal(t, uint8(sample.class), message.Values.(ipc.ClockClassValue).ClockClass)
			case ipc.TypePTPState:
				lockEvents++
			}
		}
		if previousClass != gm.ClockClass() {
			require.Equal(t, 1, classEvents)
		} else {
			require.Zero(t, classEvents)
		}
		if previousState == sample.state {
			require.Zero(t, lockEvents)
			require.Empty(t, manager.SyncStatusUpdateCh())
		}
		require.NotEmpty(t, manager.StatsUpdatesCh())
	}
}
