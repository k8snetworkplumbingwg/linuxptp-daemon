package clock

import (
	"testing"
	"time"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/stretchr/testify/require"
)

type commonHoldoverPMC struct {
	pmc.MockClient
	settings chan protocol.GrandmasterSettings
}

func (client *commonHoldoverPMC) SetGMSettings(cfgName string, settings protocol.GrandmasterSettings) error {
	err := client.MockClient.SetGMSettings(cfgName, settings)
	client.settings <- settings
	return err
}

func TestCommonTBCSlowModelTimeoutAnnouncement(t *testing.T) {
	bc, recorder := newLockedTBCClock()
	client := &commonHoldoverPMC{settings: make(chan protocol.GrandmasterSettings, 16)}
	bc.pmcClient = client
	result := bc.AddEvent(makeTBCEvent(event.Ptp4lProcessName, event.PTP_FREERUN, 10, true))
	require.Equal(t, event.PTP_HOLDOVER, result.State)
	require.Equal(t, fbprotocol.ClockClass(135), result.ClockClass)
	for index := range 3 {
		result = bc.AddEvent(event.Event{Source: event.DPLL, IFace: testTBCIface, CfgName: bc.cfgName, ClockType: event.TBC, Time: time.Now().UnixMilli() + int64(index+1), Data: &event.DPLLData{State: event.PTP_HOLDOVER, Offset: utils.ToPtr[int64](5), OutOfSpec: true, SourceLost: true, LeadingSource: true, ToFreeRunThreshold: 1500, MaxInSpecOffset: 500}})
		require.Equal(t, event.PTP_HOLDOVER, result.State)
		require.Equal(t, fbprotocol.ClockClass(165), result.ClockClass)
	}
	require.False(t, bc.leadingClockData.lastInSpec)
	found := false
	for _, message := range recorder.messages {
		if message.Type == ipc.TypeClockClass {
			if value, ok := message.Values.(ipc.ClockClassValue); ok && value.ClockClass == 165 {
				found = true
			}
		}
	}
	require.True(t, found, "class-only quality change reaches IPC")
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for targets := 0; targets < 2; {
		select {
		case settings := <-client.settings:
			if settings.ClockQuality.ClockClass == fbprotocol.ClockClass(165) {
				require.False(t, settings.TimePropertiesDS.TimeTraceable)
				targets++
			}
		case <-deadline.C:
			t.Fatal("timeout-only class165 did not reach both existing PMC targets")
		}
	}
}

func TestCommonTBCZeroLimitsAreNotUninitialized(t *testing.T) {
	bc, _ := newLockedTBCClock()
	fillBCDataWindows(bc, 0)
	for index, out := range []bool{false, true, true} {
		result := bc.AddEvent(event.Event{Source: event.DPLL, IFace: testTBCIface, CfgName: bc.cfgName, ClockType: event.TBC, Time: time.Now().UnixMilli() + int64(index+1), Data: &event.DPLLData{State: event.PTP_HOLDOVER, Offset: utils.ToPtr[int64](0), OutOfSpec: out, SourceLost: true, LeadingSource: true}})
		require.Equal(t, event.PTP_HOLDOVER, result.State)
		class := fbprotocol.ClockClass(135)
		if out {
			class = fbprotocol.ClockClass(165)
		}
		require.Equal(t, class, result.ClockClass)
	}
}

func TestCommonTBCNoPhaseLegacyFallback(t *testing.T) {
	for _, zeroLimits := range []bool{false, true} {
		t.Run(map[bool]string{false: "quality fallback", true: "zero limit fallback"}[zeroLimits], func(t *testing.T) {
			bc, _ := newLockedTBCClock()
			for _, data := range bc.data {
				if data.ProcessName == event.DPLL {
					data.Details = nil
				}
			}
			fillBCDataWindows(bc, 0)
			terminal, inSpec := uint64(1500), uint64(500)
			if zeroLimits {
				terminal, inSpec = 0, 0
				bc.leadingClockData.toFreeRunThreshold = 0
				bc.leadingClockData.MaxInSpecOffset = 0
			}
			result := bc.AddEvent(event.Event{Source: event.DPLL, IFace: testTBCIface, CfgName: bc.cfgName, ClockType: event.TBC, Time: time.Now().UnixMilli() + 1, Data: &event.DPLLData{State: event.PTP_HOLDOVER, OutOfSpec: true, SourceLost: true, LeadingSource: true, ToFreeRunThreshold: terminal, MaxInSpecOffset: inSpec}})
			if zeroLimits {
				require.Equal(t, event.PTP_FREERUN, result.State)
			} else {
				require.Equal(t, event.PTP_HOLDOVER, result.State)
				require.Equal(t, fbprotocol.ClockClass(135), result.ClockClass)
			}
		})
	}
}
