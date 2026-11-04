package dpll

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clockmgr"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/stretchr/testify/require"
)

func receiveDPLLObservation(t *testing.T, dpll *DpllConfig) *event.DataDetails {
	t.Helper()
	dpll.RLock()
	sample, mode := dpll.observationData(), dpll.processConfig.ClockType
	dpll.RUnlock()
	return receiveDPLLWire(t, mode, sample)
}

func receiveDPLLWire(t *testing.T, mode event.ClockType, sample *event.DPLLData) *event.DataDetails {
	t.Helper()
	manager := clockmgr.Init("test", make(chan event.Event, 16), nil, nil, nil, nil)
	t.Cleanup(manager.RemoveAllClocks)
	var receiver clock.Clock
	var holder interface {
		GetData(event.EventSource) *event.Data
	}
	if mode == event.TBC {
		bc, err := clock.NewTBC("ts2phc.0.config", func() int { return 37 }, &pmc.MockClient{}, "")
		require.NoError(t, err)
		bc.SetConfiguredLeadingInterface("iface")
		receiver, holder = bc, bc
	} else {
		gm, err := clock.NewGM("ts2phc.0.config", func() int { return 37 }, &pmc.MockClient{})
		require.NoError(t, err)
		receiver, holder = gm, gm
	}
	require.NoError(t, manager.AddClock(receiver))
	base := event.Event{CfgName: "ts2phc.0.config", IFace: "iface", Time: 1, ClockType: receiver.ClockType()}
	base.Source, base.Data = event.GNSS, &event.GNSSData{GPSStatus: 3}
	manager.ProcessEvent(base)
	base.Source, base.Data = event.TS2PHC, &event.OffsetData{State: event.PTP_LOCKED}
	manager.ProcessEvent(base)
	base.Source, base.Data = event.DPLL, sample
	manager.ProcessEvent(base)
	detail := holder.GetData(event.DPLL).GetDataDetails("iface")
	require.NotNil(t, detail)
	return detail
}
