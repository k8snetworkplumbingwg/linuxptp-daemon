package clockmgr

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestTGMClassOnlyHoldoverMetric(t *testing.T) {
	classes := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_tgm_class"}, []string{processLabel, nodeLabel, configLabel})
	manager := Init("node", make(chan event.Event), nil, nil, classes, nil)
	gm, err := clock.NewGM("ts2phc.0.config", func() int { return 37 }, &pmc.MockClient{})
	require.NoError(t, err)
	require.NoError(t, manager.AddClock(gm))
	manager.ProcessEvent(event.Event{Source: event.GNSS, CfgName: gm.ConfigName(), IFace: "leading", ClockType: event.GM, Time: 1, Data: &event.GNSSData{GPSStatus: 3}})
	for index, sample := range []struct {
		out   bool
		class float64
	}{{false, 7}, {true, 140}, {true, 140}} {
		manager.ProcessEvent(event.Event{Source: event.DPLL, CfgName: gm.ConfigName(), IFace: "leading", ClockType: event.GM, Time: int64(index + 2), Data: &event.DPLLData{State: event.PTP_HOLDOVER, OutOfSpec: sample.out, LeadingSource: true, Offset: utils.ToPtr[int64](500)}})
		require.Equal(t, event.PTP_HOLDOVER, gm.GetState())
		requireGaugeValue(t, classes, prometheus.Labels{processLabel: string(event.Ptp4lProcessName), nodeLabel: "node", configLabel: "ptp4l.0.config"}, sample.class)
	}
}
