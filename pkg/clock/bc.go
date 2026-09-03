package clock

import (
	"sync"
	"time"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	parserconstants "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
)

// defaultBCClockHoldOverTimeout is the amount of time a BC/OC clock holds
// HOLDOVER after its upstream source is lost before dropping to FREERUN.
const defaultBCClockHoldOverTimeout = 5 * time.Second

// BCClock is a simple Boundary Clock instance (no DPLL/ts2phc). It also backs
// the Ordinary Clock (OC) role, which is a single slave-port time receiver and
// shares the same state machine; the clockType field records which role it serves.
type BCClock struct {
	cfgName          string
	clockType        event.ClockType
	sendIPC          func(ipc.Message)
	iface            string
	data             []*event.Data
	syncState        event.PTPState
	clockClass       fbprotocol.ClockClass
	overallSyncState event.PTPState
	osClockState     event.PTPState
	// holdOverTimeout is how long to stay in HOLDOVER after the upstream
	// source is lost before falling back to FREERUN. Zero disables the
	// mini-holdover (immediate FREERUN).
	holdOverTimeout time.Duration
	// holdoverStart is when HOLDOVER was entered. A zero value means the
	// clock is not currently in HOLDOVER.
	holdoverStart time.Time
	// holdoverTimer fires the holdover->freerun transition on its own after
	// holdOverTimeout, so BC/OC exits mini-holdover even with no further
	// ptp4l events during source loss. Nil when not in HOLDOVER.
	holdoverTimer *time.Timer
	// upstreamClockClass is the last upstream grandmaster clock class
	// observed via PMC ParentDS data.
	upstreamClockClass fbprotocol.ClockClass
	// mu guards the FSM state below, which is also touched by the background
	// holdover timer goroutine.
	mu sync.Mutex
	// portRole is the latest parsed ptp4l port role (from PTPData.Values).
	// It is one of the upstream source-loss triggers: a transition away from
	// SLAVE indicates the upstream adjacency is lost. haveRole is true once a
	// role has been observed; until then role-based loss is ignored so a fresh
	// clock (zero-value portRole) is not spuriously held over.
	portRole parserconstants.PTPPortRole
	haveRole bool
}

// SetHoldOverTimeout sets how long the BC/OC stays in HOLDOVER after losing
// its upstream source before falling back to FREERUN.
func (c *BCClock) SetHoldOverTimeout(seconds int64) {
	c.holdOverTimeout = time.Duration(seconds) * time.Second
}

// ClockType returns the clock type for this clock (BC or OC).
func (c *BCClock) ClockType() event.ClockType {
	if c.clockType == event.ClockUnset {
		return event.BC
	}
	return c.clockType
}

// ClockClass returns the current clock class.
func (c *BCClock) ClockClass() fbprotocol.ClockClass { return c.clockClass }

// ConfigName returns the configuration name.
func (c *BCClock) ConfigName() string { return c.cfgName }

func (c *BCClock) getData(processName event.EventSource) *event.Data {
	for _, d := range c.data {
		if d.ProcessName == processName {
			return d
		}
	}
	d := &event.Data{ProcessName: processName, State: event.PTP_UNKNOWN, Window: *utils.NewWindow(event.WindowSize)}
	c.data = append(c.data, d)
	return d
}

// AddEvent processes an event and updates clock state.
func (c *BCClock) AddEvent(ev event.Event) SyncState {
	switch ev.Source {
	case event.PMC:
		if ds, ok := ev.Data.(*event.ParentDSData); ok {
			clockClass := fbprotocol.ClockClass(ds.ParentDataSet.GrandmasterClockClass)
			c.updateClockClass(clockClass)
		}
		return SyncState{State: c.syncState, LeadingIFace: event.LEADING_INTERFACE_UNKNOWN}
	default:
		d := c.getData(ev.Source)

		ptp, ok := ev.Data.(*event.PTPData)
		if !ok || ptp == nil {
			return SyncState{State: c.syncState, LeadingIFace: event.LEADING_INTERFACE_UNKNOWN}
		}

		if c.iface == "" && ev.IFace != "" {
			c.iface = ev.IFace
		}

		// A port-role event is a side-channel: it updates the last known
		// upstream port role (one of the source-loss triggers) but carries no
		// servo State, so it must not run through the servo Data window. The
		// offset/state events below are the ones that maintain d.State.
		roleOnly := false
		if role, ok := ptp.Values[event.PortRole]; ok {
			if r, ok := role.(int64); ok {
				c.portRole = parserconstants.PTPPortRole(r)
				c.haveRole = true
			}
			roleOnly = true
		}

		if !roleOnly {
			d.AddEvent(ev)
			d.UpdateState()
		}

		c.mu.Lock()
		c.applyMiniHoldover(c.sourceLost(d.State))
		state := c.syncState
		c.mu.Unlock()

		return SyncState{State: state, LeadingIFace: event.LEADING_INTERFACE_UNKNOWN}
	}
}

// SystemClockUpdate updates the OS clock state from an external source
// (PHC2SYS/CHRONYD). For a BC/OC the OS clock tracks the same PTP source, so
// this also folds into the overall sync state.
func (c *BCClock) SystemClockUpdate(osClockState event.PTPState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.osClockState = osClockState
	emitOverallSyncStateIfChanged(c.sendIPC, &c.overallSyncState, c.syncState, c.osClockState, c.cfgName)
}

// sourceLost reports whether the upstream PTP source is lost, combining the
// servo state and the last known port role. The source is considered lost if
// either signal indicates it: the servo degraded below LOCKED, or the upstream
// port is no longer SLAVE (e.g. FAULTY, MASTER/PASSIVE/LISTENING, or self
// elected as grand master). A port role of UNKNOWN (not yet observed) is not
// treated as lost so a freshly started clock is not spuriously held over.
func (c *BCClock) sourceLost(servo event.PTPState) bool {
	if servo != event.PTP_LOCKED {
		return true
	}
	if c.haveRole && c.portRole != parserconstants.PortRoleSlave {
		return true
	}
	return false
}

// announceClockClassLocked emits a clock_class IPC if cc differs from the last
// announced class. Assumes c.mu is held.
func (c *BCClock) announceClockClassLocked(cc fbprotocol.ClockClass) {
	if cc == c.clockClass {
		return
	}
	c.clockClass = cc
	c.sendIPC(ipc.Message{
		Type:    ipc.TypeClockClass,
		Profile: c.cfgName,
		IFace:   c.iface,
		Values:  ipc.ClockClassValue{ClockClass: uint8(cc)},
	})
}

// setStateLocked applies a mini-holdover state transition, emitting the
// corresponding ptp_state and clock_class IPCs plus the overall sync_state.
// The os_clock_state IPC is owned by the PHC2SYS/CHRONYD path
// (SystemClockUpdate) and is not synthesised here from the PTP state. Assumes
// c.mu is held.
func (c *BCClock) setStateLocked(newState event.PTPState) {
	if c.syncState == newState {
		return
	}
	c.syncState = newState

	c.sendIPC(ipc.Message{
		Type:    ipc.TypePTPState,
		Profile: c.cfgName,
		IFace:   c.iface,
		Values:  ipc.StateValue{State: event.PtpStateToIPCState(newState)},
	})

	switch newState {
	case event.PTP_HOLDOVER:
		// In holdover the BC no longer tracks the primary reference: report
		// the G.8275.1 holdover clock class (135), not the locked class.
		c.announceClockClassLocked(protocol.ClockClassHoldover)
	case event.PTP_FREERUN:
		c.announceClockClassLocked(protocol.ClockClassFreerun)
	case event.PTP_LOCKED:
		if c.upstreamClockClass != 0 {
			c.announceClockClassLocked(c.upstreamClockClass)
		}
	}

	emitOverallSyncStateIfChanged(c.sendIPC, &c.overallSyncState, c.syncState, c.osClockState, c.cfgName)
}

// cancelHoldoverTimer stops any armed holdover timer.
func (c *BCClock) cancelHoldoverTimer() {
	if c.holdoverTimer != nil {
		c.holdoverTimer.Stop()
		c.holdoverTimer = nil
	}
}

// armHoldoverTimer schedules the holdover->freerun transition after
// holdOverTimeout. No timer is armed when holdOverTimeout is <= 0.
func (c *BCClock) armHoldoverTimer() {
	c.cancelHoldoverTimer()
	if c.holdOverTimeout <= 0 {
		return
	}
	c.holdoverTimer = time.AfterFunc(c.holdOverTimeout, c.holdoverExpired)
}

// holdoverExpired runs on the timer goroutine and drops the clock to FREERUN
// when holdOverTimeout elapses while still in HOLDOVER. This lets the clock
// exit mini-holdover even when no further ptp4l events arrive during source
// loss.
func (c *BCClock) holdoverExpired() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.syncState != event.PTP_HOLDOVER {
		return
	}
	c.holdoverStart = time.Time{}
	c.cancelHoldoverTimer()
	c.setStateLocked(event.PTP_FREERUN)
}

// applyMiniHoldover runs the BC/OC mini-holdover FSM. When the upstream PTP
// source is lost, the clock first reports HOLDOVER for holdOverTimeout and only
// then falls back to FREERUN. If the source is re-acquired during holdover, the
// clock returns to LOCKED immediately. A never-synced FREERUN stays FREERUN
// (there is nothing to hold over). The FREERUN fallback is driven by a
// self-arming timer so it fires without further events. Assumes c.mu is held.
func (c *BCClock) applyMiniHoldover(sourceLost bool) {
	now := time.Now()
	if !sourceLost {
		// Source present: clear holdover and lock.
		c.cancelHoldoverTimer()
		c.holdoverStart = time.Time{}
		c.setStateLocked(event.PTP_LOCKED)
		return
	}
	if c.holdoverStart.IsZero() {
		// Enter holdover only if previously synchronized; otherwise a
		// never-synced clock stays FREERUN.
		if c.syncState == event.PTP_LOCKED && c.holdOverTimeout > 0 {
			c.holdoverStart = now
			c.armHoldoverTimer()
			c.setStateLocked(event.PTP_HOLDOVER)
		} else {
			c.setStateLocked(event.PTP_FREERUN)
		}
		return
	}
	// Already in holdover: the timer handles the timeout transition. Keep
	// HOLDOVER while within the window; only fall through here if the timeout
	// is disabled or already elapsed (e.g. a very short timeout racing ahead of
	// the timer goroutine).
	if c.holdOverTimeout <= 0 || now.Sub(c.holdoverStart) >= c.holdOverTimeout {
		c.cancelHoldoverTimer()
		c.holdoverStart = time.Time{}
		c.setStateLocked(event.PTP_FREERUN)
	} else {
		c.setStateLocked(event.PTP_HOLDOVER)
	}
}

// Reset resets the clock state.
func (c *BCClock) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancelHoldoverTimer()
	c.syncState = event.PTP_FREERUN
	c.overallSyncState = event.PTP_FREERUN
	c.clockClass = 0
	c.upstreamClockClass = 0
	c.data = nil
	c.iface = ""
	c.holdoverStart = time.Time{}
	c.portRole = parserconstants.PortRoleUnknown
	c.haveRole = false
}

// updateClockClass records the upstream grandmaster clock class observed via
// PMC ParentDS data. It is announced while LOCKED; during a mini-holdover the
// announced class reflects HOLDOVER (135) or FREERUN (248), and on recovery the
// FSM re-announces the upstream class.
func (c *BCClock) updateClockClass(clockClass fbprotocol.ClockClass) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.upstreamClockClass = clockClass
	if c.syncState == event.PTP_LOCKED {
		c.announceClockClassLocked(clockClass)
	}
}
