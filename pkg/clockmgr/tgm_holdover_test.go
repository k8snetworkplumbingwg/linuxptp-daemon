package clockmgr

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/alias"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
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
		expected := float64(25)
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
