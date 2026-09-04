package clock

import (
	"testing"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestBCClock() (*BCClock, *ipcRecorder) {
	rio := &ipcRecorder{}
	return &BCClock{
		cfgName:          testPTP4lCfg,
		sendIPC:          rio.send,
		syncState:        event.PTP_NOTSET,
		overallSyncState: event.PTP_NOTSET,
		osClockState:     event.PTP_NOTSET,
	}, rio
}

func TestBCClock_AddEvent_StateTransitions(t *testing.T) {
	t.Run("NOTSET to LOCKED emits ptp_state IPC", func(t *testing.T) {
		bc, rio := newTestBCClock()
		cs := bc.AddEvent(event.Event{
			IFace: testEns7f0,
			Data:  &event.OffsetData{State: event.PTP_LOCKED},
		})
		assert.Equal(t, event.PTP_LOCKED, cs.State)
		require.Len(t, rio.messages, 1)
		assert.Equal(t, ipc.TypePTPState, rio.messages[0].Type)
		assert.Equal(t, testPTP4lCfg, rio.messages[0].Profile)
		assert.Equal(t, testEns7f0, rio.messages[0].IFace)
		assert.Equal(t, ipc.StateValue{State: ipc.StateLocked}, rio.messages[0].Values)
	})

	t.Run("LOCKED to FREERUN emits ptp_state IPC", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.syncState = event.PTP_LOCKED
		cs := bc.AddEvent(event.Event{
			IFace: testEns7f0,
			Data:  &event.OffsetData{State: event.PTP_FREERUN},
		})
		assert.Equal(t, event.PTP_FREERUN, cs.State)
		require.Len(t, rio.messages, 1)
		assert.Equal(t, ipc.StateFreerun, rio.messages[0].Values.(ipc.StateValue).State)
	})

	t.Run("duplicate state does not emit ptp_state IPC", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.syncState = event.PTP_LOCKED
		cs := bc.AddEvent(event.Event{
			IFace: testEns7f0,
			Data:  &event.OffsetData{State: event.PTP_LOCKED},
		})
		assert.Equal(t, event.PTP_LOCKED, cs.State)
		assert.Empty(t, rio.messages)
	})

	t.Run("nil Data returns current state unchanged", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.syncState = event.PTP_FREERUN
		cs := bc.AddEvent(event.Event{Data: nil})
		assert.Equal(t, event.PTP_FREERUN, cs.State)
		assert.Empty(t, rio.messages)
	})
}

func TestBCClock_UpdateOSClockState(t *testing.T) {
	t.Run("worst of LOCKED and FREERUN is FREERUN", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.syncState = event.PTP_LOCKED
		bc.overallSyncState = event.PTP_LOCKED
		bc.SystemClockUpdate(event.PTP_FREERUN)
		assert.Equal(t, event.PTP_FREERUN, bc.overallSyncState)
		assert.Equal(t, event.PTP_FREERUN, bc.osClockState)
		require.Len(t, rio.messages, 1)
		assert.Equal(t, ipc.TypeSyncState, rio.messages[0].Type)
		assert.Equal(t, ipc.SyncStateValue{State: ipc.StateFreerun}, rio.messages[0].Values)
	})

	t.Run("worst of LOCKED and LOCKED is LOCKED", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.syncState = event.PTP_LOCKED
		bc.overallSyncState = event.PTP_LOCKED
		bc.SystemClockUpdate(event.PTP_LOCKED)
		assert.Equal(t, event.PTP_LOCKED, bc.overallSyncState)
		assert.Empty(t, rio.messages)
	})

	t.Run("worst of HOLDOVER and LOCKED is HOLDOVER", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.syncState = event.PTP_HOLDOVER
		bc.overallSyncState = event.PTP_NOTSET
		bc.SystemClockUpdate(event.PTP_LOCKED)
		assert.Equal(t, event.PTP_HOLDOVER, bc.overallSyncState)
		require.Len(t, rio.messages, 1)
		assert.Equal(t, ipc.TypeSyncState, rio.messages[0].Type)
	})

	t.Run("no change does not emit IPC", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.syncState = event.PTP_FREERUN
		bc.overallSyncState = event.PTP_FREERUN
		bc.SystemClockUpdate(event.PTP_LOCKED)
		assert.Equal(t, event.PTP_FREERUN, bc.overallSyncState)
		assert.Empty(t, rio.messages)
	})
}

func TestBCClock_UpdateClockClass(t *testing.T) {
	t.Run("change emits clock_class IPC with iface", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.AddEvent(event.Event{IFace: testEns7f0, Data: &event.OffsetData{State: event.PTP_LOCKED}})
		rio.messages = nil // clear ptp_state IPC from addEvent
		bc.updateClockClass(fbprotocol.ClockClass6)
		require.Len(t, rio.messages, 1)
		assert.Equal(t, ipc.TypeClockClass, rio.messages[0].Type)
		assert.Equal(t, testPTP4lCfg, rio.messages[0].Profile)
		assert.Equal(t, testEns7f0, rio.messages[0].IFace)
		assert.Equal(t, ipc.ClockClassValue{ClockClass: 6}, rio.messages[0].Values)
	})

	t.Run("same class does not emit IPC", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.clockClass = fbprotocol.ClockClass6
		bc.updateClockClass(fbprotocol.ClockClass6)
		assert.Empty(t, rio.messages)
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
	assert.Equal(t, event.BC, bc.ClockType())
	assert.Equal(t, testPTP4lCfg, bc.ConfigName())
}

func TestBCClock_ClockType(t *testing.T) {
	t.Run("unset defaults to BC", func(t *testing.T) {
		bc, _ := newTestBCClock()
		assert.Equal(t, event.BC, bc.ClockType())
	})

	t.Run("OC role is reported as OC", func(t *testing.T) {
		bc, _ := newTestBCClock()
		bc.clockType = event.OC
		assert.Equal(t, event.OC, bc.ClockType())
	})

	t.Run("OC clock processes events like BC", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.clockType = event.OC
		cs := bc.AddEvent(event.Event{
			IFace: testEns7f0,
			Data:  &event.OffsetData{State: event.PTP_LOCKED},
		})
		assert.Equal(t, event.PTP_LOCKED, cs.State)
		require.Len(t, rio.messages, 1)
		assert.Equal(t, ipc.TypePTPState, rio.messages[0].Type)
	})
}

func TestBCClock_ParentDSUpdate(t *testing.T) {
	t.Run("updates clock class and emits", func(t *testing.T) {
		rio := &ipcRecorder{}
		bc := &BCClock{cfgName: testPTP4lCfg, sendIPC: rio.send}

		parentDS := protocol.ParentDataSet{
			GrandmasterClockClass: 6,
		}
		bc.AddEvent(event.Event{Source: event.PMC, Data: &event.ParentDSData{ParentDataSet: parentDS}})

		assert.Equal(t, fbprotocol.ClockClass(6), bc.clockClass)
		require.Len(t, rio.messages, 1)
		assert.Equal(t, ipc.TypeClockClass, rio.messages[0].Type)
		assert.Equal(t, ipc.ClockClassValue{ClockClass: 6}, rio.messages[0].Values)
	})

	t.Run("unchanged class does not send IPC", func(t *testing.T) {
		rio := &ipcRecorder{}
		bc := &BCClock{cfgName: testPTP4lCfg, sendIPC: rio.send, clockClass: fbprotocol.ClockClass(6)}

		parentDS := protocol.ParentDataSet{
			GrandmasterClockClass: 6,
		}
		bc.AddEvent(event.Event{Source: event.PMC, Data: &event.ParentDSData{ParentDataSet: parentDS}})

		assert.Empty(t, rio.messages, "updateClockClass should no-op on unchanged class")
	})
}

func TestBCClock_AddEvent_ProcessStatusDoesNotCreateData(t *testing.T) {
	bc, rio := newTestBCClock()
	cs := bc.AddEvent(event.ProcessStatusEvent(event.PTP4l, testPTP4lCfg, event.OC, "", 1))
	assert.Equal(t, event.PTP_NOTSET, cs.State)
	data := bc.ProcessData()
	require.Len(t, data, 1)
	assert.Equal(t, event.PTP4l, data[0].ProcessName)
	assert.Equal(t, event.PTP_UNKNOWN, data[0].State)
	assert.Empty(t, data[0].Details)
	assert.Equal(t, int64(1), data[0].ProcessStatus)
	assert.Empty(t, rio.messages)
}
