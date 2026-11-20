package clockmgr

import (
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/alias"
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

func TestCommonTBCTimeoutClassMetric(t *testing.T) {
	classes := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_shared_tbc_class"}, []string{processLabel, nodeLabel, configLabel})
	offsets := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_shared_tbc_offset"}, []string{fromLabel, processLabel, nodeLabel, ifaceLabel})
	manager := Init("node", make(chan event.Event, 64), offsets, nil, classes, nil)
	bc, err := clock.NewTBC("ptp4l.shared.config", func() int { return 37 }, &pmc.MockClient{}, "ptp4l.controlled.config")
	require.NoError(t, err)
	bc.SetConfiguredLeadingInterface("leading")
	require.NoError(t, manager.AddClock(bc))
	base := time.Now().UnixMilli()
	for index := range event.WindowSize + 2 {
		manager.ProcessEvent(event.Event{Source: event.DPLL, CfgName: bc.ConfigName(), IFace: "leading", ClockType: event.TBC, Time: base + int64(index), Data: &event.DPLLData{State: event.PTP_LOCKED, Offset: utils.ToPtr[int64](5), LeadingSource: true, InSyncConditionThreshold: 100, InSyncConditionTimes: 1, ToFreeRunThreshold: 100, MaxInSpecOffset: 40}})
		manager.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: bc.ConfigName(), IFace: "leading", ClockType: event.TBC, Time: base + int64(index), Data: &event.OffsetData{State: event.PTP_LOCKED, Offset: 5}})
	}
	require.Equal(t, event.PTP_LOCKED, bc.GetState())
	for index, out := range []bool{false, true, true} {
		manager.ProcessEvent(event.Event{Source: event.DPLL, CfgName: bc.ConfigName(), IFace: "leading", ClockType: event.TBC, Time: base + int64(event.WindowSize+index+3), Data: &event.DPLLData{State: event.PTP_HOLDOVER, Offset: utils.ToPtr[int64](5), LeadingSource: true, SourceLost: true, OutOfSpec: out, InSyncConditionThreshold: 100, InSyncConditionTimes: 1, ToFreeRunThreshold: 100, MaxInSpecOffset: 40}})
		require.Equal(t, event.PTP_HOLDOVER, bc.GetState())
		class := float64(135)
		if out {
			class = 165
		}
		requireGaugeValue(t, classes, prometheus.Labels{processLabel: string(event.Ptp4lProcessName), nodeLabel: "node", configLabel: bc.ConfigName()}, class)
		requireGaugeValue(t, offsets, prometheus.Labels{fromLabel: string(event.TBC), processLabel: string(event.TBC), nodeLabel: "node", ifaceLabel: alias.GetAlias("leading")}, 5)
	}
}
