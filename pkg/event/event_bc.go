package event

import (
	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
)

// BCClock is a simple Boundary Clock instance (no DPLL/ts2phc).
// State is derived directly from ptp4l clock state codes and offset
// thresholds, matching CEPv1 behavior.
type BCClock struct {
	cfgName          string
	io               clockIO
	iface            string
	syncState        PTPState
	clockClass       fbprotocol.ClockClass
	overallSyncState PTPState
	osClockState     PTPState
}

// ClockType returns BC.
func (c *BCClock) ClockType() ClockType { return BC }

func (c *BCClock) ClockClass() fbprotocol.ClockClass {
	return c.clockClass
}

// ConfigName returns the config profile name.
func (c *BCClock) ConfigName() string { return c.cfgName }

// BCProcessResult holds the outcome of BCClock.addEvent.
type BCProcessResult struct {
	State            PTPState
	SyncStateChanged bool
}

func (c *BCClock) addEvent(event Event) BCProcessResult {
	ptp, ok := event.Data.(*PTPData)
	if !ok || ptp == nil {
		return BCProcessResult{State: c.syncState}
	}

	if c.iface == "" && event.IFace != "" {
		c.iface = event.IFace
	}

	prev := c.syncState
	c.syncState = ptp.State
	changed := c.syncState != prev

	if changed {
		c.io.sendIPC(ipc.Message{
			Type:    ipc.TypePTPState,
			Profile: c.cfgName,
			IFace:   event.IFace,
			Values:  ipc.StateValue{State: ptpStateToIPCState(c.syncState)},
		})
	}

	emitOverallSyncStateIfChanged(c.io, &c.overallSyncState, c.syncState, c.osClockState, c.cfgName)

	return BCProcessResult{
		State:            c.syncState,
		SyncStateChanged: changed,
	}
}

func (c *BCClock) SystemClockUpdate(osClockState PTPState) {
	c.osClockState = osClockState
	emitOverallSyncStateIfChanged(c.io, &c.overallSyncState, c.syncState, c.osClockState, c.cfgName)
}

func (c *BCClock) ParentDSUpdate(parentDS protocol.ParentDataSet) {
	clockClass := fbprotocol.ClockClass(parentDS.GrandmasterClockClass)
	c.updateClockClass(clockClass)
	c.io.emitClockClass(clockClass, c.cfgName)
}

func (c *BCClock) updateClockClass(clockClass fbprotocol.ClockClass) {
	if clockClass == c.clockClass {
		return
	}
	c.clockClass = clockClass
	c.io.sendIPC(ipc.Message{
		Type:    ipc.TypeClockClass,
		Profile: c.cfgName,
		IFace:   c.iface,
		Values:  ipc.ClockClassValue{ClockClass: uint8(clockClass)},
	})
}
