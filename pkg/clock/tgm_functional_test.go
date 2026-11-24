package clock

import (
	"errors"
	"fmt"
	"testing"
	"time"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/stretchr/testify/require"
)

func TestTGMHoldoverAccuracySurvivesOtherEventsAndPMCRetry(t *testing.T) {
	gm, _, client := newTestGMClock()
	gm.data = []*event.Data{gmData(event.GNSS, event.PTP_FREERUN), gmData(event.Ts2PhcProcessName, event.PTP_FREERUN)}
	client.SetGMSettingsErr = errors.New("PMC unavailable")
	desired := fbprotocol.ClockAccuracyFromOffset(2 * time.Microsecond)
	result := gm.AddEvent(event.Event{Source: event.DPLL, IFace: testTBCIface, Time: 10, Data: &event.DPLLData{State: event.PTP_HOLDOVER, OutOfSpec: true, LeadingSource: true, Offset: utils.ToPtr[int64](2000)}})
	require.Equal(t, desired, result.ClockAccuracy)
	require.NotEqual(t, protocol.ClockClassOutOfSpec, gm.announcedClockClass)
	client.SetGMSettingsErr = nil
	for _, incoming := range []event.Event{
		{Source: event.GNSS, IFace: testTBCIface, Time: 11, Data: &event.GNSSData{}},
		{Source: event.DPLL, IFace: testTBCIface, Time: 9, Data: &event.DPLLData{State: event.PTP_HOLDOVER, Offset: utils.ToPtr[int64](10)}},
		{Source: event.DPLL, IFace: "secondary", Time: 12, Data: &event.DPLLData{State: event.PTP_HOLDOVER, Offset: utils.ToPtr[int64](10)}},
	} {
		result = gm.AddEvent(incoming)
		require.Equal(t, protocol.ClockClassOutOfSpec, result.ClockClass)
		require.Equal(t, desired, result.ClockAccuracy)
	}
	calls := client.SnapshotSetCalls()
	require.Len(t, calls, 2)
	require.Equal(t, desired, calls[1].GMSettings.ClockQuality.ClockAccuracy)
	require.Equal(t, desired, gm.announcedClockAccuracy)
	result = gm.AddEvent(event.Event{Source: event.DPLL, IFace: testTBCIface, Time: 13, Data: &event.DPLLData{State: event.PTP_HOLDOVER, OutOfSpec: true, LeadingSource: true}})
	require.Equal(t, fbprotocol.ClockAccuracyUnknown, result.ClockAccuracy)
	require.Len(t, client.SnapshotSetCalls(), 3)
}

func TestTGMFunctionalHoldoverPMCAndClassOnlyIPC(t *testing.T) {
	gm, rio, client := newTestGMClock()
	gm.data = []*event.Data{gmData(event.GNSS, event.PTP_FREERUN), gmData(event.Ts2PhcProcessName, event.PTP_FREERUN)}
	client.GMSettingsResult.TimePropertiesDS.Leap61 = true
	for index, sample := range []struct {
		state  event.PTPState
		out    bool
		offset int64
		class  fbprotocol.ClockClass
	}{
		{event.PTP_HOLDOVER, false, 10, fbprotocol.ClockClass7},
		{event.PTP_HOLDOVER, true, 500, protocol.ClockClassOutOfSpec},
		{event.PTP_HOLDOVER, true, 2000, protocol.ClockClassOutOfSpec},
		{event.PTP_FREERUN, true, 99999999999, protocol.ClockClassFreerun},
		{event.PTP_LOCKED, false, 0, protocol.ClockClassFreerun},
	} {
		if sample.state == event.PTP_LOCKED {
			gm.GetData(event.GNSS).State = event.PTP_LOCKED
			gm.GetData(event.Ts2PhcProcessName).State = event.PTP_LOCKED
			sample.class = fbprotocol.ClockClass6
		}
		before := len(rio.messages)
		result := gm.AddEvent(event.Event{Source: event.DPLL, IFace: testTBCIface, CfgName: gm.cfgName, ClockType: event.GM, Time: int64(index + 1), Data: &event.DPLLData{State: sample.state, OutOfSpec: sample.out, Offset: utils.ToPtr(sample.offset), LeadingSource: true, FrequencyTraceable: sample.state == event.PTP_FREERUN}})
		require.Equal(t, sample.state, result.State)
		require.Equal(t, sample.class, result.ClockClass)
		calls := client.SnapshotSetCalls()
		require.Len(t, calls, index+1)
		settings := calls[index].GMSettings
		require.Equal(t, sample.class, settings.ClockQuality.ClockClass)
		require.Equal(t, sample.class == fbprotocol.ClockClass6 || sample.class == fbprotocol.ClockClass7, settings.TimePropertiesDS.TimeTraceable)
		require.Equal(t, sample.class != protocol.ClockClassFreerun, settings.TimePropertiesDS.FrequencyTraceable)
		require.True(t, settings.TimePropertiesDS.Leap61)
		require.Equal(t, int32(stubUtcOffset()), settings.TimePropertiesDS.CurrentUtcOffset)
		if sample.state == event.PTP_HOLDOVER {
			require.Equal(t, fbprotocol.ClockAccuracyFromOffset(time.Duration(sample.offset)*time.Nanosecond), settings.ClockQuality.ClockAccuracy)
		}
		if index == 1 {
			var classes, states int
			for _, message := range rio.messages[before:] {
				if message.Type == ipc.TypeClockClass {
					classes++
				}
				if message.Type == ipc.TypePTPState {
					states++
				}
			}
			require.Equal(t, 1, classes)
			require.Zero(t, states)
		}
		client.GMSettingsResult = *settings
	}
}

func TestTGMAnnouncementRepairsStaleFrequencyTraceability(t *testing.T) {
	for _, clockClass := range []fbprotocol.ClockClass{fbprotocol.ClockClass6, fbprotocol.ClockClass7, protocol.ClockClassOutOfSpec, protocol.ClockClassFreerun} {
		t.Run(fmt.Sprintf("class_%d", clockClass), func(t *testing.T) {
			gm, _, client := newTestGMClock()
			gm.syncState.ClockClass = clockClass
			announcedClass, announcedAccuracy, err := gm.announceClockClass()
			require.NoError(t, err)
			calls := client.SnapshotSetCalls()
			require.Len(t, calls, 1)
			require.Equal(t, clockClass, announcedClass)
			require.Equal(t, calls[0].GMSettings.ClockQuality.ClockAccuracy, announcedAccuracy)
			client.GMSettingsResult = *calls[0].GMSettings
			client.GMSettingsResult.TimePropertiesDS.FrequencyTraceable = clockClass == protocol.ClockClassFreerun
			_, _, err = gm.announceClockClass()
			require.NoError(t, err)
			calls = client.SnapshotSetCalls()
			require.Len(t, calls, 2)
			require.Equal(t, clockClass, calls[1].GMSettings.ClockQuality.ClockClass)
			require.Equal(t, clockClass != protocol.ClockClassFreerun, calls[1].GMSettings.TimePropertiesDS.FrequencyTraceable)
			client.GMSettingsResult = *calls[1].GMSettings
			_, _, err = gm.announceClockClass()
			require.NoError(t, err)
			require.Len(t, client.SnapshotSetCalls(), 2)
		})
	}
}
