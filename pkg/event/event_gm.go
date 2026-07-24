package event

import (
	"fmt"
	"strings"
	"time"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/debug"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
)

func (c *GMClock) ParentDSUpdate(_ protocol.ParentDataSet) {}

func (c *GMClock) getData(processName EventSource) *Data {
	for _, d := range c.data {
		if d.ProcessName == processName {
			return d
		}
	}
	d := &Data{ProcessName: processName, State: PTP_UNKNOWN, window: *utils.NewWindow(WindowSize)}
	c.data = append(c.data, d)
	return d
}

func (c *GMClock) addEvent(event Event) (*DataDetails, clockSyncState) {
	d := c.getData(event.Source)
	d.AddEvent(event)
	d.UpdateState()
	clockState := c.updateState()
	emitOverallSyncStateIfChanged(c.io, &c.overallSyncState, c.syncState.state, c.osClockState, c.cfgName)
	c.announceClockClassIfChanged(event, clockState)
	return d.GetDataDetails(event.IFace), clockState
}

func (c *GMClock) announceClockClassIfChanged(event Event, clockState clockSyncState) {
	if clockState.clockClass == protocol.ClockClassUninitialized {
		return
	}

	clockAccuracy := c.announcedClockAccuracy
	if ptp, isPTP := event.Data.(*PTPData); isPTP && event.Source == DPLL {
		if clockState.clockClass == fbprotocol.ClockClass7 || clockState.clockClass == protocol.ClockClassOutOfSpec {
			if offset, found := ptp.Values[OFFSET]; found {
				if offsetValue, isInt64 := offset.(int64); isInt64 {
					clockAccuracy = fbprotocol.ClockAccuracyFromOffset(time.Duration(offsetValue) * time.Nanosecond)
				}
			}
		}
	}

	if clockState.clockClass != c.announcedClockClass || clockAccuracy != c.announcedClockAccuracy {
		glog.Infof("clock class change request from %d to %d with clock accuracy from %d to %d",
			uint8(c.announcedClockClass), uint8(clockState.clockClass),
			uint8(c.announcedClockAccuracy), uint8(clockAccuracy))
		c.syncState.clockAccuracy = clockAccuracy
		err, clockClass, _ := c.announceClockClass()
		debug.UpdateClockClass(uint8(clockState.clockClass))
		if err != nil {
			glog.Errorf("error updating clock class %s", err)
		} else {
			glog.Infof("updated clock class to %d", clockClass)
			c.io.emitClockClass(clockClass, c.cfgName)
			clockClassOut := utils.GetClockClassLogMessage(PTP4lProcessName, c.cfgName, clockClass)
			fmt.Printf("%s", clockClassOut)
		}
	}

	if c.lastLoggedState != clockState.state {
		glog.Infof("PTP State: %v, Clock Class %d Time %s sourceLost %v",
			clockState.state, clockState.clockClass, time.Now(), clockState.sourceLost)
		c.lastLoggedState = clockState.state
	}
}

func (c *GMClock) isSourceLost() bool {
	for _, d := range c.data {
		if d.ProcessName == GNSS && len(d.Details) > 0 && d.Details[0] != nil {
			return d.Details[0].sourceLost
		}
	}
	return false
}

func (c *GMClock) getLeadingInterface() string {
	for _, d := range c.data {
		if d.ProcessName == GNSS && len(d.Details) > 0 {
			return d.Details[0].IFace
		} else if d.ProcessName == TS2PHCProcessName && len(d.Details) > 0 {
			for _, dd := range d.Details {
				if dd.signalSource == GNSS {
					return dd.IFace
				}
			}
		}
	}
	return LEADING_INTERFACE_UNKNOWN
}

func (c *GMClock) hasNonLeadingDPLLFault(leadingInterface string) bool {
	if leadingInterface == LEADING_INTERFACE_UNKNOWN {
		return false
	}
	for _, d := range c.data {
		if d.ProcessName != DPLL {
			continue
		}
		leadingDetail := d.GetDataDetails(leadingInterface)
		if leadingDetail == nil || leadingDetail.State != PTP_LOCKED {
			return false
		}
		for _, dd := range d.Details {
			if dd.IFace != leadingInterface && dd.State != PTP_LOCKED {
				glog.Infof("non-leading DPLL %s is %s while leading %s is locked, composite DPLL forced to FREERUN",
					dd.IFace, dd.State, leadingInterface)
				return true
			}
		}
	}
	return false
}

func (c *GMClock) SystemClockUpdate(osClockState PTPState) {
	c.osClockState = osClockState
	emitOverallSyncStateIfChanged(c.io, &c.overallSyncState, c.syncState.state, c.osClockState, c.cfgName)
}

func (c *GMClock) updateClockClass(clockClass fbprotocol.ClockClass) {
	if clockClass == c.syncState.clockClass {
		return
	}
	c.syncState.clockClass = clockClass
	c.io.sendIPC(ipc.Message{
		Type:    ipc.TypeClockClass,
		Profile: c.cfgName,
		IFace:   c.syncState.leadingIFace,
		Values:  ipc.ClockClassValue{ClockClass: uint8(clockClass)},
	})
}

func (c *GMClock) announceClockClass() (err error, clockClass fbprotocol.ClockClass, clockAccuracy fbprotocol.ClockAccuracy) {
	pmcCfgName := strings.Replace(c.cfgName, TS2PHCProcessName, PTP4lProcessName, 1)
	g, err := c.io.getGMSettings(pmcCfgName)
	if err != nil {
		glog.Errorf("failed to get current GRANDMASTER_SETTINGS_NP: %s", err)
		return err, clockClass, clockAccuracy
	}
	g.TimePropertiesDS.PtpTimescale = true
	g.TimePropertiesDS.FrequencyTraceable = true
	g.TimePropertiesDS.CurrentUtcOffsetValid = true
	g.TimePropertiesDS.CurrentUtcOffset = int32(c.io.getUtcOffset())
	switch c.syncState.clockClass {
	case fbprotocol.ClockClass6:
		if g.ClockQuality.ClockClass != fbprotocol.ClockClass6 || g.TimePropertiesDS.TimeTraceable != true {
			g.ClockQuality.ClockClass = fbprotocol.ClockClass6
			g.TimePropertiesDS.TimeTraceable = true
			g.ClockQuality.ClockAccuracy = fbprotocol.ClockAccuracyNanosecond100
			g.TimePropertiesDS.TimeSource = fbprotocol.TimeSourceGNSS
			g.ClockQuality.OffsetScaledLogVariance = 0x4e5d
			err = c.io.setGMSettings(pmcCfgName, g)
		}
	case protocol.ClockClassOutOfSpec:
		if g.ClockQuality.ClockClass != protocol.ClockClassOutOfSpec {
			g.ClockQuality.ClockClass = protocol.ClockClassOutOfSpec
			g.TimePropertiesDS.TimeTraceable = false
			g.ClockQuality.ClockAccuracy = c.syncState.clockAccuracy
			g.TimePropertiesDS.TimeSource = fbprotocol.TimeSourceInternalOscillator
			g.ClockQuality.OffsetScaledLogVariance = 0xffff
			err = c.io.setGMSettings(pmcCfgName, g)
		}
	case fbprotocol.ClockClass7:
		if g.ClockQuality.ClockClass != fbprotocol.ClockClass7 {
			g.ClockQuality.ClockClass = fbprotocol.ClockClass7
			g.TimePropertiesDS.TimeTraceable = true
			g.ClockQuality.ClockAccuracy = c.syncState.clockAccuracy
			g.TimePropertiesDS.TimeSource = fbprotocol.TimeSourceInternalOscillator
			g.ClockQuality.OffsetScaledLogVariance = 0xffff
			err = c.io.setGMSettings(pmcCfgName, g)
		}
	case protocol.ClockClassFreerun:
		if g.ClockQuality.ClockClass != protocol.ClockClassFreerun {
			g.ClockQuality.ClockClass = protocol.ClockClassFreerun
			g.TimePropertiesDS.TimeTraceable = false
			g.ClockQuality.ClockAccuracy = fbprotocol.ClockAccuracyUnknown
			g.TimePropertiesDS.TimeSource = fbprotocol.TimeSourceInternalOscillator
			g.ClockQuality.OffsetScaledLogVariance = 0xffff
			err = c.io.setGMSettings(pmcCfgName, g)
		}
	default:
		glog.Infof("No clock class identified for %d", c.syncState.clockClass)
		err = fmt.Errorf("no clock class identified for %d", c.syncState.clockClass)
	}
	if err == nil {
		c.announcedClockClass = g.ClockQuality.ClockClass
		c.announcedClockAccuracy = g.ClockQuality.ClockAccuracy
	}
	return err, g.ClockQuality.ClockClass, g.ClockQuality.ClockAccuracy
}

// getGMState ... get lowest state of all the interfaces
/*
GNSS State + DPLL State= DPLL State
DPLL STate + Ts2phc State =GM State
----------------------------------------------------------------
GNSS| Mode              | Offset   | State
1.  | 0-2(Source LOST)  | in Range | FREERUN
2.  | 0-2(Source LOST ) | out Range| FREERUN
3.  | 3                 | in Range | LOCKED
4.  | 3                 | out Range| FREERUN
----------------------------------------------------------------
DPLL | Frequency/Phase  	|  Offset  | GNSS STATE |  DPLL PTP STATE
------------------------------------------------------------------
1.  | -1/1/0           	| in Range |  LOCKED    | FREERUN
2.  | -1/1/0           	| out Range|  FREERUN   | FREERUN
-----------------------------------------------------------------
3.  |  2 (LOCKED)       	| in Range |  LOCKED      | LOCKED
4.  |  2 (LOCKED)       	| in Range |  FREERUN     | LOCKED
-----------------------------------------------------------------
SL :-> Source Lost
------------------------------------------------------------------------------------------
DPLL| Frequency/Phase      | Offset      | GNSS STATE               | DPLL PTP State
------------------------------------------------------------------------------------------
5   | 2 (LOCKED)           | Out Range   | All State                | FREERUN
6.  | 3 (LOCK_ACQ_HOLDOVER)| In Range    | LOCKED                   | LOCKED
7.  | 3 (LOCK_ACQ_HOLDOVER)| In/Out Range| FREERUN (SL)             | FREERUN
8.  | 3 (LOCK_ACQ_HOLDOVER)| Out Range   | LOCKED                   | FREERUN
*9. | 3 (LOCK_ACQ_HOLDOVER)| In/Out Range| FREERUN (SL)             | HOLDOVER
------------------------------------------------------------------------------------------
*10.| 4 (HOLDOVER)		| IN/Out Range   | FREERUN (SL)	            | HOLDOVER
*11.| 4 (HOLDOVER)		| in/Out Range   | FREERUN (SL)             | AFTER TIME OUT
                                                                    FREERUN OUT OF SPEC

12. | 4 (HOLDOVER)		| in Range	     | LOCKED                   | LOCKED
13. | 4 (HOLDOVER)		| Out Range	     | LOCKED                   | FREERUN
14. | 4 (HOLDOVER)		| in Range       | FREERUN (SL)             | LOCKED
15. | 4 (HOLDOVER)		| Out Range      | FREERUN (SL)             | FREERUN
------------------------------------------------------------------------------------------
FINAL GM STATE  *SL = Source Lost
---------------------------------------------------------------------------------------------
| DPLL PTP State        | GNSS PTP STATE    | TS2PHC PTP STATE | GM STATE  | Clock Class
---------------------------------------------------------------------------------------------
| FREERUN               | NA                | NA                | FREERUN  | 248
| HOLDOVER IN SPEC      | NA                | NA                | HOLDOVER | 7
| FREERUN OUT OF SPEC   | NA                | NA                | FREERUN  | 140
| LOCKED                | LOCKED            | LOCKED            | LOCKED   | 6
| LOCKED                | LOCKED            | FREERUN           | FREERUN  | 248
| LOCKED                | *FREERUN (SL)     | LOCKED            | NA       | Wait for DPLL
                                                                           | to move to HOLDOVER

| LOCKED                | *FREERUN (SL)     | FREERUN           | NA       | Wait for DPLL
                                                                           |to move to HOLDOVER

| LOCKED                | *FREERUN(offset)  | LOCKED            | FREERUN  | 248
| LOCKED                | *FREERUN(offset)  | FREERUN           | FREERUN  | 248
 Final GM State When DPLL not available
---------------------------------------------------------------------------------------------
DPLL PTP State |  GNSS PTP STATE  |	TS2PHC PTP STATE| GM STATE | Clock Class
---------------------------------------------------------------------------------------------
| NA           |  FREERUN         |	LOCKED          | FREERUN  | 248
| NA           |  FREERUN         |	FREERUN         | FREERUN  | 248
| NA           |  LOCKED          |	FREERUN         | FREERUN  | 248
| NA           |  LOCKED          |	LOCKED          | LOCKED   | 6

*/
// updateState computes the composite GM state from DPLL, GNSS, and ts2phc
// data sources. It emits TypePTPState and TypeClockClass IPC on state/class change.
func (c *GMClock) updateState() clockSyncState {
	dpllState := PTP_NOTSET
	gnssState := PTP_FREERUN
	ts2phcState := PTP_FREERUN
	syncSrcLost := c.isSourceLost()
	leadingInterface := c.getLeadingInterface()
	if leadingInterface == LEADING_INTERFACE_UNKNOWN {
		glog.Infof("Leading interface is not yet identified, clock state reporting delayed.")
		return clockSyncState{leadingIFace: leadingInterface}
	}

	c.syncState.sourceLost = syncSrcLost
	c.syncState.leadingIFace = leadingInterface

	var outOfSpec, frequencyTraceable bool
	if c.data != nil {
		for _, d := range c.data {
			switch d.ProcessName {
			case DPLL:
				dpllState = d.State
				if c.hasNonLeadingDPLLFault(leadingInterface) {
					dpllState = PTP_FREERUN
				}
				if dd := d.GetDataDetails(leadingInterface); dd != nil {
					outOfSpec = dd.outOfSpec
					frequencyTraceable = dd.frequencyTraceable
				}
			case GNSS:
				gnssState = d.State
			case TS2PHCProcessName:
				ts2phcState = d.State
				if parser.NoSourceTSCount == 2 {
					ts2phcState = PTP_FREERUN
				}
			}
		}
	} else {
		c.syncState.state = PTP_FREERUN
		c.syncState.clockClass = protocol.ClockClassFreerun
		c.syncState.clockAccuracy = fbprotocol.ClockAccuracyUnknown
		c.syncState.lastLoggedTime = time.Now().Unix()
		c.syncState.clkLog = fmt.Sprintf("%s[%d]:[%s] %s T-GM-STATUS %s\n", GM, c.syncState.lastLoggedTime, c.cfgName, leadingInterface, c.syncState.state)
		return c.syncState
	}

	prevState := c.syncState.state
	prevClass := c.syncState.clockClass

	switch dpllState {
	case PTP_FREERUN:
		c.syncState.state = dpllState
		if outOfSpec && frequencyTraceable {
			c.syncState.clockClass = protocol.ClockClassOutOfSpec
		} else {
			c.syncState.clockClass = protocol.ClockClassFreerun
		}
		c.syncState.clockAccuracy = fbprotocol.ClockAccuracyUnknown
	case PTP_HOLDOVER:
		c.syncState.state = dpllState
		c.syncState.clockClass = fbprotocol.ClockClass7
	case PTP_LOCKED, PTP_NOTSET:
		switch gnssState {
		case PTP_LOCKED:
			switch ts2phcState {
			case PTP_FREERUN:
				c.syncState.state = PTP_FREERUN
				c.syncState.clockClass = protocol.ClockClassFreerun
				c.syncState.clockAccuracy = fbprotocol.ClockAccuracyUnknown
			case PTP_LOCKED:
				c.syncState.state = PTP_LOCKED
				c.syncState.clockClass = fbprotocol.ClockClass6
				c.syncState.clockAccuracy = fbprotocol.ClockAccuracyNanosecond100
			case PTP_HOLDOVER:
				c.syncState.state = PTP_HOLDOVER
				c.syncState.clockClass = fbprotocol.ClockClass7
			}
		case PTP_FREERUN:
			if syncSrcLost {
				switch ts2phcState {
				case PTP_LOCKED:
				case PTP_FREERUN:
					c.syncState.state = PTP_FREERUN
				case PTP_HOLDOVER:
					c.syncState.state = PTP_HOLDOVER
					c.syncState.clockClass = fbprotocol.ClockClass7
				}
			} else {
				switch ts2phcState {
				case PTP_FREERUN, PTP_LOCKED, PTP_UNKNOWN, PTP_NOTSET:
					c.syncState.state = PTP_FREERUN
					c.syncState.clockClass = protocol.ClockClassFreerun
					c.syncState.clockAccuracy = fbprotocol.ClockAccuracyUnknown
				}
			}
		}
	default:
		switch gnssState {
		case PTP_LOCKED:
			switch ts2phcState {
			case PTP_FREERUN, PTP_UNKNOWN, PTP_NOTSET:
				c.syncState.state = PTP_FREERUN
				c.syncState.clockClass = protocol.ClockClassFreerun
				c.syncState.clockAccuracy = fbprotocol.ClockAccuracyUnknown
			case PTP_LOCKED:
				c.syncState.state = PTP_LOCKED
				c.syncState.clockClass = fbprotocol.ClockClass6
				c.syncState.clockAccuracy = fbprotocol.ClockAccuracyNanosecond100
			case PTP_HOLDOVER:
				c.syncState.state = PTP_HOLDOVER
				c.syncState.clockClass = fbprotocol.ClockClass7
			}
		case PTP_FREERUN:
			switch ts2phcState {
			case PTP_FREERUN, PTP_LOCKED, PTP_UNKNOWN, PTP_NOTSET:
				c.syncState.state = PTP_FREERUN
				c.syncState.clockClass = protocol.ClockClassFreerun
				c.syncState.clockAccuracy = fbprotocol.ClockAccuracyUnknown
			case PTP_HOLDOVER:
				c.syncState.state = PTP_HOLDOVER
				c.syncState.clockClass = fbprotocol.ClockClass7
			}
		default:
			c.syncState.state = ts2phcState
			switch ts2phcState {
			case PTP_FREERUN:
				c.syncState.clockClass = protocol.ClockClassFreerun
				c.syncState.clockAccuracy = fbprotocol.ClockAccuracyUnknown
			case PTP_LOCKED:
				c.syncState.clockClass = fbprotocol.ClockClass7
				c.syncState.clockAccuracy = fbprotocol.ClockAccuracyNanosecond100
			}
		}
	}

	if gnssState != c.gnssState {
		c.gnssState = gnssState
		c.io.sendIPC(ipc.Message{
			Type:    ipc.TypeGNSSState,
			Profile: c.cfgName,
			IFace:   leadingInterface,
			Values:  ipc.GNSSStateValue{State: ptpStateToIPCState(gnssState)},
		})
	}

	// TODO: the prevState != PTP_NOTSET guard suppresses the initial FREERUN
	// IPC announcement. TBCClock and BCClock do NOT have this guard, so they
	// report from PTP_NOTSET. Verify whether GM should also emit the first
	// transition or if suppressing it is correct for multi-source init.
	if c.syncState.state != prevState && prevState != PTP_NOTSET {
		c.io.sendIPC(ipc.Message{
			Type:    ipc.TypePTPState,
			Profile: c.cfgName,
			IFace:   leadingInterface,
			Values:  ipc.StateValue{State: ptpStateToIPCState(c.syncState.state)},
		})
	}

	if c.syncState.clockClass != prevClass && prevClass != protocol.ClockClassUninitialized {
		c.io.sendIPC(ipc.Message{
			Type:    ipc.TypeClockClass,
			Profile: c.cfgName,
			IFace:   leadingInterface,
			Values:  ipc.ClockClassValue{ClockClass: uint8(c.syncState.clockClass)},
		})
	}

	result := clockSyncState{
		state:         c.syncState.state,
		clockClass:    c.syncState.clockClass,
		clockAccuracy: c.syncState.clockAccuracy,
		sourceLost:    c.syncState.sourceLost,
		leadingIFace:  c.syncState.leadingIFace,
	}

	logTime := time.Now().Unix()
	if c.syncState.lastLoggedTime != logTime {
		clkLog := fmt.Sprintf("%s[%d]:[%s] %s T-GM-STATUS %s\n", GM, logTime, c.cfgName, leadingInterface, c.syncState.state)
		c.syncState.lastLoggedTime = logTime
		c.syncState.clkLog = clkLog
		result.clkLog = clkLog
		glog.Infof("dpll State %s, gnss State %s, tsphc state %s, gm state %s,", dpllState, gnssState, ts2phcState, c.syncState.state)
	}
	return result
}
