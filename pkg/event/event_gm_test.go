package event

import (
	"fmt"
	"testing"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestGMClock() (*GMClock, *recordingClockIO) {
	rio := &recordingClockIO{}
	gm := &GMClock{
		cfgName: "ts2phc.0.config",
		io:      rio,
		syncState: clockSyncState{
			state:         PTP_NOTSET,
			clockClass:    protocol.ClockClassUninitialized,
			clockAccuracy: fbprotocol.ClockAccuracyUnknown,
		},
		overallSyncState: PTP_NOTSET,
		osClockState:     PTP_NOTSET,
		gnssState:        PTP_NOTSET,
	}
	return gm, rio
}

func gmData(process EventSource, state PTPState) *Data {
	d := &Data{
		ProcessName: process,
		State:       state,
		Details: []*DataDetails{{
			IFace: "ens1f0",
			State: state,
		}},
	}
	if process == TS2PHCProcessName {
		d.Details[0].signalSource = GNSS
	}
	return d
}

func TestGMClock_UpdateState_IPCEmission(t *testing.T) {
	const cfg = "ts2phc.0.config"
	const iface = "ens1f0"

	t.Run("FREERUN to LOCKED emits TypePTPState", func(t *testing.T) {
		gm, rio := newTestGMClock()

		// First call: establish FREERUN (from PTP_NOTSET, no IPC emitted)
		gm.data = []*Data{
			gmData(GNSS, PTP_LOCKED),
			gmData(DPLL, PTP_LOCKED),
			gmData(TS2PHCProcessName, PTP_FREERUN),
		}
		result := gm.updateState()
		assert.Equal(t, PTP_FREERUN, result.state)

		var gotGNSS bool
		for _, msg := range rio.ipcMessages {
			assert.NotEqual(t, ipc.TypePTPState, msg.Type, "no TypePTPState IPC from PTP_NOTSET→FREERUN")
			if msg.Type == ipc.TypeGNSSState {
				gv := msg.Values.(ipc.GNSSStateValue)
				assert.Equal(t, ipc.StateLocked, gv.State)
				assert.Equal(t, cfg, msg.Profile)
				assert.Equal(t, iface, msg.IFace)
				gotGNSS = true
			}
		}
		assert.True(t, gotGNSS, "expected TypeGNSSState LOCKED on initial GNSS transition")
		rio.ipcMessages = nil

		// Second call: transition to LOCKED
		gm.data[2] = gmData(TS2PHCProcessName, PTP_LOCKED)
		result = gm.updateState()
		assert.Equal(t, PTP_LOCKED, result.state)
		assert.Equal(t, fbprotocol.ClockClass6, result.clockClass)

		var gotState, gotClass6 bool
		for _, msg := range rio.ipcMessages {
			if msg.Type == ipc.TypePTPState {
				sv := msg.Values.(ipc.StateValue)
				assert.Equal(t, ipc.StateLocked, sv.State)
				assert.Equal(t, cfg, msg.Profile)
				gotState = true
			}
			if msg.Type == ipc.TypeClockClass {
				cv := msg.Values.(ipc.ClockClassValue)
				assert.Equal(t, uint8(6), cv.ClockClass)
				gotClass6 = true
			}
			assert.NotEqual(t, ipc.TypeGNSSState, msg.Type, "no GNSS IPC when GNSS state unchanged")
		}
		assert.True(t, gotState, "expected TypePTPState LOCKED")
		assert.True(t, gotClass6, "expected TypeClockClass 6 on FREERUN→LOCKED")
	})

	t.Run("LOCKED to HOLDOVER emits TypePTPState and TypeClockClass", func(t *testing.T) {
		gm, rio := newTestGMClock()

		// Establish FREERUN then LOCKED
		gm.data = []*Data{
			gmData(GNSS, PTP_LOCKED),
			gmData(DPLL, PTP_LOCKED),
			gmData(TS2PHCProcessName, PTP_FREERUN),
		}
		gm.updateState()
		gm.data[2] = gmData(TS2PHCProcessName, PTP_LOCKED)
		gm.updateState()
		rio.ipcMessages = nil // clear

		// DPLL holdover
		gm.data[1] = gmData(DPLL, PTP_HOLDOVER)
		result := gm.updateState()
		assert.Equal(t, PTP_HOLDOVER, result.state)
		assert.Equal(t, fbprotocol.ClockClass7, result.clockClass)

		var gotState, gotClass bool
		for _, msg := range rio.ipcMessages {
			if msg.Type == ipc.TypePTPState {
				sv := msg.Values.(ipc.StateValue)
				assert.Equal(t, ipc.StateHoldover, sv.State)
				gotState = true
			}
			if msg.Type == ipc.TypeClockClass {
				cv := msg.Values.(ipc.ClockClassValue)
				assert.Equal(t, uint8(7), cv.ClockClass)
				gotClass = true
			}
			assert.NotEqual(t, ipc.TypeGNSSState, msg.Type, "no GNSS IPC when GNSS state unchanged")
		}
		assert.True(t, gotState, "expected TypePTPState HOLDOVER")
		assert.True(t, gotClass, "expected TypeClockClass 7")
	})

	t.Run("duplicate state produces no IPC", func(t *testing.T) {
		gm, rio := newTestGMClock()

		gm.data = []*Data{
			gmData(GNSS, PTP_LOCKED),
			gmData(DPLL, PTP_LOCKED),
			gmData(TS2PHCProcessName, PTP_FREERUN),
		}
		gm.updateState()
		gm.updateState()
		rio.ipcMessages = nil

		// Same state again
		gm.updateState()
		assert.Empty(t, rio.ipcMessages, "duplicate events should not emit IPC")
	})

	t.Run("GNSS LOCKED to FREERUN emits TypeGNSSState", func(t *testing.T) {
		gm, rio := newTestGMClock()

		// Establish GNSS LOCKED
		gm.data = []*Data{
			gmData(GNSS, PTP_LOCKED),
			gmData(DPLL, PTP_LOCKED),
			gmData(TS2PHCProcessName, PTP_FREERUN),
		}
		gm.updateState()
		rio.ipcMessages = nil

		// GNSS drops to FREERUN
		gm.data[0] = gmData(GNSS, PTP_FREERUN)
		gm.updateState()

		var gotGNSS bool
		for _, msg := range rio.ipcMessages {
			if msg.Type == ipc.TypeGNSSState {
				gv := msg.Values.(ipc.GNSSStateValue)
				assert.Equal(t, ipc.StateFreerun, gv.State)
				assert.Equal(t, cfg, msg.Profile)
				assert.Equal(t, iface, msg.IFace)
				gotGNSS = true
			}
		}
		assert.True(t, gotGNSS, "expected TypeGNSSState FREERUN when GNSS drops")
	})

	t.Run("LOCKED to FREERUN emits TypeClockClass 248", func(t *testing.T) {
		gm, rio := newTestGMClock()

		// Establish LOCKED
		gm.data = []*Data{
			gmData(GNSS, PTP_LOCKED),
			gmData(DPLL, PTP_LOCKED),
			gmData(TS2PHCProcessName, PTP_FREERUN),
		}
		gm.updateState()
		gm.data[2] = gmData(TS2PHCProcessName, PTP_LOCKED)
		gm.updateState()
		rio.ipcMessages = nil

		// ts2phc FREERUN → GM FREERUN
		gm.data[2] = gmData(TS2PHCProcessName, PTP_FREERUN)
		result := gm.updateState()
		assert.Equal(t, PTP_FREERUN, result.state)
		assert.Equal(t, protocol.ClockClassFreerun, result.clockClass)

		var gotClass bool
		for _, msg := range rio.ipcMessages {
			if msg.Type == ipc.TypeClockClass {
				cv := msg.Values.(ipc.ClockClassValue)
				assert.Equal(t, uint8(protocol.ClockClassFreerun), cv.ClockClass)
				gotClass = true
			}
		}
		assert.True(t, gotClass, "expected TypeClockClass 248")
	})
}

func TestGMClock_UpdateOSClockState(t *testing.T) {
	gm, rio := newTestGMClock()
	gm.syncState.state = PTP_LOCKED

	gm.SystemClockUpdate(PTP_LOCKED)
	assert.Equal(t, PTP_LOCKED, gm.overallSyncState, "first call from PTP_NOTSET should change")
	assert.Equal(t, PTP_LOCKED, gm.osClockState)
	require.Len(t, rio.ipcMessages, 1)
	assert.Equal(t, ipc.TypeSyncState, rio.ipcMessages[0].Type)

	rio.ipcMessages = nil
	gm.SystemClockUpdate(PTP_LOCKED)
	assert.Empty(t, rio.ipcMessages, "same state should not emit IPC")

	gm.SystemClockUpdate(PTP_FREERUN)
	assert.Equal(t, PTP_FREERUN, gm.overallSyncState, "OS clock FREERUN should degrade overall")
	require.Len(t, rio.ipcMessages, 1)
	assert.Equal(t, ipc.TypeSyncState, rio.ipcMessages[0].Type)
	assert.Equal(t, ipc.SyncStateValue{State: ipc.StateFreerun}, rio.ipcMessages[0].Values)
}

func TestGMClock_UpdateClockClass(t *testing.T) {
	gm, rio := newTestGMClock()
	gm.syncState.clockClass = fbprotocol.ClockClass6
	gm.syncState.leadingIFace = "ens1f0"

	// Same class → no IPC
	gm.updateClockClass(fbprotocol.ClockClass6)
	assert.Empty(t, rio.ipcMessages)

	// Different class → IPC emitted
	gm.updateClockClass(fbprotocol.ClockClass7)
	assert.Len(t, rio.ipcMessages, 1)
	assert.Equal(t, ipc.TypeClockClass, rio.ipcMessages[0].Type)
	cv := rio.ipcMessages[0].Values.(ipc.ClockClassValue)
	assert.Equal(t, uint8(7), cv.ClockClass)
}

func TestGMClock_AnnounceClockClassIfChanged(t *testing.T) {
	t.Run("skips when clockClass is uninitialized", func(t *testing.T) {
		gm, rio := newTestGMClock()
		gm.announceClockClassIfChanged(
			Event{Source: DPLL, Data: &PTPData{Values: map[ValueType]interface{}{OFFSET: int64(50)}}},
			clockSyncState{state: PTP_LOCKED, clockClass: protocol.ClockClassUninitialized},
		)
		assert.Empty(t, rio.setGMCalls)
	})

	t.Run("sets GM settings on class change", func(t *testing.T) {
		gm, rio := newTestGMClock()
		gm.announcedClockClass = fbprotocol.ClockClass6
		gm.announcedClockAccuracy = fbprotocol.ClockAccuracyNanosecond100
		gm.syncState.clockClass = protocol.ClockClassFreerun
		gm.announceClockClassIfChanged(
			Event{Source: GNSS, Data: &GNSSData{}},
			clockSyncState{state: PTP_FREERUN, clockClass: protocol.ClockClassFreerun},
		)
		require.Len(t, rio.setGMCalls, 1)
		assert.Equal(t, protocol.ClockClassFreerun, rio.setGMCalls[0].ClockQuality.ClockClass)
		assert.Equal(t, fbprotocol.ClockAccuracyUnknown, rio.setGMCalls[0].ClockQuality.ClockAccuracy)
	})

	t.Run("no PMC call when class and accuracy unchanged", func(t *testing.T) {
		gm, rio := newTestGMClock()
		gm.announcedClockClass = fbprotocol.ClockClass6
		gm.announcedClockAccuracy = fbprotocol.ClockAccuracyNanosecond100
		gm.syncState.clockClass = fbprotocol.ClockClass6
		gm.announceClockClassIfChanged(
			Event{Source: GNSS, Data: &GNSSData{}},
			clockSyncState{state: PTP_LOCKED, clockClass: fbprotocol.ClockClass6},
		)
		assert.Empty(t, rio.setGMCalls)
	})

	t.Run("DPLL holdover computes accuracy from offset", func(t *testing.T) {
		gm, rio := newTestGMClock()
		gm.announcedClockClass = fbprotocol.ClockClass6
		gm.announcedClockAccuracy = fbprotocol.ClockAccuracyNanosecond100
		gm.syncState.clockClass = fbprotocol.ClockClass7
		gm.announceClockClassIfChanged(
			Event{Source: DPLL, Data: &PTPData{Values: map[ValueType]interface{}{OFFSET: int64(500)}}},
			clockSyncState{state: PTP_HOLDOVER, clockClass: fbprotocol.ClockClass7},
		)
		require.Len(t, rio.setGMCalls, 1)
		assert.Equal(t, fbprotocol.ClockClass7, rio.setGMCalls[0].ClockQuality.ClockClass)
		assert.NotEqual(t, fbprotocol.ClockAccuracyUnknown, rio.setGMCalls[0].ClockQuality.ClockAccuracy)
	})

	t.Run("updates announcedClockClass on success", func(t *testing.T) {
		gm, _ := newTestGMClock()
		gm.announcedClockClass = fbprotocol.ClockClass6
		gm.announcedClockAccuracy = fbprotocol.ClockAccuracyNanosecond100
		gm.syncState.clockClass = protocol.ClockClassFreerun
		gm.announceClockClassIfChanged(
			Event{Source: GNSS, Data: &GNSSData{}},
			clockSyncState{state: PTP_FREERUN, clockClass: protocol.ClockClassFreerun},
		)
		assert.Equal(t, protocol.ClockClassFreerun, gm.announcedClockClass)
	})

	t.Run("does not update announcedClockClass on setGMSettings error", func(t *testing.T) {
		gm, rio := newTestGMClock()
		rio.setGMErr = fmt.Errorf("pmc error")
		gm.announcedClockClass = fbprotocol.ClockClass6
		gm.announcedClockAccuracy = fbprotocol.ClockAccuracyNanosecond100
		gm.syncState.clockClass = protocol.ClockClassFreerun
		gm.announceClockClassIfChanged(
			Event{Source: GNSS, Data: &GNSSData{}},
			clockSyncState{state: PTP_FREERUN, clockClass: protocol.ClockClassFreerun},
		)
		assert.Equal(t, fbprotocol.ClockClass6, gm.announcedClockClass,
			"announcedClockClass must not change when PMC write fails")
	})

	t.Run("emits clock class on success", func(t *testing.T) {
		gm, rio := newTestGMClock()
		gm.announcedClockClass = fbprotocol.ClockClass6
		gm.announcedClockAccuracy = fbprotocol.ClockAccuracyNanosecond100
		gm.syncState.clockClass = protocol.ClockClassFreerun
		gm.announceClockClassIfChanged(
			Event{Source: GNSS, Data: &GNSSData{}},
			clockSyncState{state: PTP_FREERUN, clockClass: protocol.ClockClassFreerun},
		)
		require.Len(t, rio.emittedClasses, 1)
		assert.Equal(t, protocol.ClockClassFreerun, rio.emittedClasses[0])
	})
}
