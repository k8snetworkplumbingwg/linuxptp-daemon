package event

import (
	"testing"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestBCClock() (*BCClock, *recordingClockIO) {
	rio := &recordingClockIO{}
	return &BCClock{
		cfgName:          testPTP4lCfg,
		io:               rio,
		syncState:        PTP_NOTSET,
		overallSyncState: PTP_NOTSET,
		osClockState:     PTP_NOTSET,
	}, rio
}

func TestBCClock_AddEvent_StateTransitions(t *testing.T) {
	t.Run("NOTSET to LOCKED emits ptp_state IPC", func(t *testing.T) {
		bc, rio := newTestBCClock()
		result := bc.addEvent(Event{
			IFace: testEns7f0,
			Data:  &PTPData{State: PTP_LOCKED},
		})
		assert.Equal(t, PTP_LOCKED, result.State)
		assert.True(t, result.SyncStateChanged)
		require.Len(t, rio.ipcMessages, 1)
		assert.Equal(t, ipc.TypePTPState, rio.ipcMessages[0].Type)
		assert.Equal(t, testPTP4lCfg, rio.ipcMessages[0].Profile)
		assert.Equal(t, testEns7f0, rio.ipcMessages[0].IFace)
		assert.Equal(t, ipc.StateValue{State: ipc.StateLocked}, rio.ipcMessages[0].Values)
	})

	t.Run("LOCKED to FREERUN emits ptp_state IPC", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.syncState = PTP_LOCKED
		result := bc.addEvent(Event{
			IFace: testEns7f0,
			Data:  &PTPData{State: PTP_FREERUN},
		})
		assert.Equal(t, PTP_FREERUN, result.State)
		assert.True(t, result.SyncStateChanged)
		require.Len(t, rio.ipcMessages, 1)
		assert.Equal(t, ipc.StateFreerun, rio.ipcMessages[0].Values.(ipc.StateValue).State)
	})

	t.Run("duplicate state does not emit ptp_state IPC", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.syncState = PTP_LOCKED
		result := bc.addEvent(Event{
			IFace: testEns7f0,
			Data:  &PTPData{State: PTP_LOCKED},
		})
		assert.Equal(t, PTP_LOCKED, result.State)
		assert.False(t, result.SyncStateChanged)
		assert.Empty(t, rio.ipcMessages)
	})

	t.Run("nil PTPData returns current state unchanged", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.syncState = PTP_FREERUN
		result := bc.addEvent(Event{Data: nil})
		assert.Equal(t, PTP_FREERUN, result.State)
		assert.False(t, result.SyncStateChanged)
		assert.Empty(t, rio.ipcMessages)
	})
}

func TestBCClock_UpdateOSClockState(t *testing.T) {
	t.Run("worst of LOCKED and FREERUN is FREERUN", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.syncState = PTP_LOCKED
		bc.overallSyncState = PTP_LOCKED
		bc.SystemClockUpdate(PTP_FREERUN)
		assert.Equal(t, PTP_FREERUN, bc.overallSyncState)
		assert.Equal(t, PTP_FREERUN, bc.osClockState)
		require.Len(t, rio.ipcMessages, 1)
		assert.Equal(t, ipc.TypeSyncState, rio.ipcMessages[0].Type)
		assert.Equal(t, ipc.SyncStateValue{State: ipc.StateFreerun}, rio.ipcMessages[0].Values)
	})

	t.Run("worst of LOCKED and LOCKED is LOCKED", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.syncState = PTP_LOCKED
		bc.overallSyncState = PTP_LOCKED
		bc.SystemClockUpdate(PTP_LOCKED)
		assert.Equal(t, PTP_LOCKED, bc.overallSyncState)
		assert.Empty(t, rio.ipcMessages)
	})

	t.Run("worst of HOLDOVER and LOCKED is HOLDOVER", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.syncState = PTP_HOLDOVER
		bc.overallSyncState = PTP_NOTSET
		bc.SystemClockUpdate(PTP_LOCKED)
		assert.Equal(t, PTP_HOLDOVER, bc.overallSyncState)
		require.Len(t, rio.ipcMessages, 1)
		assert.Equal(t, ipc.TypeSyncState, rio.ipcMessages[0].Type)
	})

	t.Run("no change does not emit IPC", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.syncState = PTP_FREERUN
		bc.overallSyncState = PTP_FREERUN
		bc.SystemClockUpdate(PTP_LOCKED)
		assert.Equal(t, PTP_FREERUN, bc.overallSyncState)
		assert.Empty(t, rio.ipcMessages)
	})
}

func TestBCClock_UpdateClockClass(t *testing.T) {
	t.Run("change emits clock_class IPC with iface", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.addEvent(Event{IFace: testEns7f0, Data: &PTPData{State: PTP_LOCKED}})
		rio.ipcMessages = nil // clear ptp_state IPC from addEvent
		bc.updateClockClass(fbprotocol.ClockClass6)
		require.Len(t, rio.ipcMessages, 1)
		assert.Equal(t, ipc.TypeClockClass, rio.ipcMessages[0].Type)
		assert.Equal(t, testPTP4lCfg, rio.ipcMessages[0].Profile)
		assert.Equal(t, testEns7f0, rio.ipcMessages[0].IFace)
		assert.Equal(t, ipc.ClockClassValue{ClockClass: 6}, rio.ipcMessages[0].Values)
	})

	t.Run("same class does not emit IPC", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.clockClass = fbprotocol.ClockClass6
		bc.updateClockClass(fbprotocol.ClockClass6)
		assert.Empty(t, rio.ipcMessages)
	})

	t.Run("class change updates stored value", func(t *testing.T) {
		bc, _ := newTestBCClock()
		bc.updateClockClass(fbprotocol.ClockClass7)
		assert.Equal(t, fbprotocol.ClockClass7, bc.clockClass)
		bc.updateClockClass(fbprotocol.ClockClass6)
		assert.Equal(t, fbprotocol.ClockClass6, bc.clockClass)
	})
}

func TestBCClock_Interface(t *testing.T) {
	bc, _ := newTestBCClock()
	assert.Equal(t, BC, bc.ClockType())
	assert.Equal(t, testPTP4lCfg, bc.ConfigName())
}
