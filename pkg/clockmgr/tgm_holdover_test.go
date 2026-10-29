package clockmgr

import (
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/alias"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestTGMHoldoverClassOnlyMetricsAndIPC(t *testing.T) {
	stateGauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_tgm_state"}, []string{"process", "node", "iface"})
	classGauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_tgm_class"}, []string{"process", "node", "config"})
	cache := ipc.NewCache(64)
	manager := Init("test-node", make(chan event.Event, 64), nil, stateGauge, classGauge, cache)
	gm, err := clock.NewGM("ts2phc.0.config", func() int { return 37 }, &pmc.MockClient{})
	require.NoError(t, err)
	require.NoError(t, manager.AddClock(gm))
	base := event.Event{CfgName: "ts2phc.0.config", IFace: "ens2f0", ClockType: event.GM, Time: 1}
	base.Source, base.Data = event.GNSS, &event.GNSSData{GPSStatus: 3}
	manager.ProcessEvent(base)
	base.Source, base.Data = event.TS2PHC, &event.OffsetData{State: event.PTP_LOCKED}
	manager.ProcessEvent(base)
	for _, sample := range []struct {
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
		base.Source, base.Data = event.DPLL, &event.DPLLData{State: sample.state, OutOfSpec: sample.out, LeadingSource: true}
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
