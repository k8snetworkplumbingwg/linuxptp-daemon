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
	"github.com/stretchr/testify/require"
)

func TestGMHoldoverQualityAndClassOnlyEvents(t *testing.T) {
	gm, recorder, _ := newTestGMClock()
	gm.data = []*event.Data{
		gmData(event.GNSS, event.PTP_LOCKED), gmData(event.TS2PHC, event.PTP_LOCKED),
	}
	for _, sample := range []struct {
		state event.PTPState
		out   bool
		class fbprotocol.ClockClass
	}{
		{event.PTP_LOCKED, false, fbprotocol.ClockClass6},
		{event.PTP_HOLDOVER, false, fbprotocol.ClockClass7},
		{event.PTP_HOLDOVER, true, protocol.ClockClassOutOfSpec},
		{event.PTP_HOLDOVER, true, protocol.ClockClassOutOfSpec},
		{event.PTP_LOCKED, false, fbprotocol.ClockClass6},
		{event.PTP_HOLDOVER, false, fbprotocol.ClockClass7},
		{event.PTP_FREERUN, true, protocol.ClockClassFreerun},
	} {
		previousState, previousClass := gm.syncState.State, gm.syncState.ClockClass
		recorder.messages = nil
		state := gm.AddEvent(event.Event{
			Source: event.DPLL, IFace: testTBCIface, CfgName: testTS2PHCCfg,
			ClockType: event.GM, Time: time.Now().UnixMilli(),
			Data: &event.DPLLData{State: sample.state, OutOfSpec: sample.out, FrequencyTraceable: true, LeadingSource: true},
		})
		require.Equal(t, sample.state, state.State)
		require.Equal(t, sample.class, state.ClockClass)
		if previousClass != sample.class {
			message := findMessage(recorder.messages, ipc.TypeClockClass)
			require.NotNil(t, message)
			require.Equal(t, uint8(sample.class), message.Values.(ipc.ClockClassValue).ClockClass)
		} else {
			require.Nil(t, findMessage(recorder.messages, ipc.TypeClockClass))
		}
		if previousState == sample.state {
			require.Nil(t, findMessage(recorder.messages, ipc.TypePTPState))
			require.Nil(t, findMessage(recorder.messages, ipc.TypeSyncState))
		}
	}
}

func TestGMAnnounceTraceabilityMatrix(t *testing.T) {
	for _, sample := range []struct {
		class fbprotocol.ClockClass
		time  bool
		freq  bool
	}{
		{fbprotocol.ClockClass6, true, true}, {fbprotocol.ClockClass7, true, true},
		{protocol.ClockClassOutOfSpec, false, true}, {protocol.ClockClassFreerun, false, false},
	} {
		t.Run(fmt.Sprintf("class%d", sample.class), func(t *testing.T) {
			gm, _, mock := newTestGMClock()
			gm.syncState.ClockClass = sample.class
			gm.announcedClockClass = sample.class
			mock.GMSettingsResult.ClockQuality.ClockClass = sample.class
			mock.GMSettingsResult.TimePropertiesDS.TimeTraceable = !sample.time
			mock.GMSettingsResult.TimePropertiesDS.FrequencyTraceable = !sample.freq
			gm.announceClockClassIfChanged(event.Event{}, gm.syncState)
			calls := mock.SnapshotSetCalls()
			require.Len(t, calls, 1)
			desired := *calls[0].GMSettings
			require.Equal(t, sample.time, desired.TimePropertiesDS.TimeTraceable)
			require.Equal(t, sample.freq, desired.TimePropertiesDS.FrequencyTraceable)
			mock.GMSettingsResult = desired
			getCalls := len(mock.SnapshotGetCalls())
			gm.announceClockClassIfChanged(event.Event{}, gm.syncState)
			require.Len(t, mock.SnapshotSetCalls(), 1, "correct settings must not be rewritten")
			require.Len(t, mock.SnapshotGetCalls(), getCalls, "steady samples must not poll PMC repeatedly")
			gm.lastTraceabilityCheck = time.Now().Add(-time.Second)
			gm.announceClockClassIfChanged(event.Event{}, gm.syncState)
			require.Len(t, mock.SnapshotGetCalls(), getCalls+1, "same-class verification resumes after one second")
		})
	}
}

func TestGMAnnounceRetriesFailedSameClassCorrection(t *testing.T) {
	gm, _, mock := newTestGMClock()
	gm.syncState.ClockClass = fbprotocol.ClockClass7
	gm.announcedClockClass = fbprotocol.ClockClass7
	mock.GMSettingsResult.ClockQuality.ClockClass = fbprotocol.ClockClass7
	mock.SetGMSettingsErr = errors.New("PMC unavailable")
	gm.announceClockClassIfChanged(event.Event{}, gm.syncState)
	require.Len(t, mock.SnapshotSetCalls(), 1)
	mock.SetGMSettingsErr = nil
	gm.announceClockClassIfChanged(event.Event{}, gm.syncState)
	require.Len(t, mock.SnapshotSetCalls(), 2)
	require.True(t, mock.SnapshotSetCalls()[1].GMSettings.TimePropertiesDS.TimeTraceable)
}
