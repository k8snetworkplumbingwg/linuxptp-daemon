package clock

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/golang/glog"
)

const (
	// FaultyPhaseOffset is a value assigned to the phase offset when free-running
	FaultyPhaseOffset int64 = 99999999999
	// StaleEventAfter is the number of milliseconds after which an event is considered stale
	StaleEventAfter int64 = 2000
)

// LeadingClockParams ... leading clock parameters includes state
// and configuration of the system leading clock. There is only
// one leading clock in the system. The leading clock is the clock that
// receives phase, frequency and ToD synchronization from an external source.
// Currently used for T-BC only
type LeadingClockParams struct {
	upstreamTimeProperties        *protocol.TimePropertiesDS
	upstreamParentDataSet         *protocol.ParentDataSet
	upstreamCurrentDSStepsRemoved uint16

	downstreamTimeProperties *protocol.TimePropertiesDS
	downstreamParentDataSet  *protocol.ParentDataSet

	leadingInterface         string
	controlledPortsConfig    string
	inSyncConditionThreshold int
	inSyncConditionTimes     int
	toFreeRunThreshold       int
	MaxInSpecOffset          uint64
	lastInSpec               bool
	inSyncThresholdCounter   int
	clockID                  string
}

func newLeadingClockParams() *LeadingClockParams {
	return &LeadingClockParams{
		upstreamParentDataSet:    &protocol.ParentDataSet{},
		upstreamTimeProperties:   &protocol.TimePropertiesDS{},
		downstreamParentDataSet:  &protocol.ParentDataSet{},
		downstreamTimeProperties: &protocol.TimePropertiesDS{},
	}
}

// TBC is a Telco Boundary Clock instance.
type TBC struct {
	cfgName                string
	sendIPC                func(ipc.Message)
	getUtcOffset           func() int
	pmcClient              pmc.Client
	syncState              SyncState
	overallSyncState       event.PTPState
	osClockState           event.PTPState
	announcedClockClass    fbprotocol.ClockClass
	announcedClockAccuracy fbprotocol.ClockAccuracy
	data                   []*event.Data
	leadingClockData       *LeadingClockParams
	downstreamCancel       context.CancelFunc
}

// ClockType returns the clock type for this TBC clock.
func (c *TBC) ClockType() event.ClockType { return event.TBC }

// ClockClass returns the current clock class.
func (c *TBC) ClockClass() fbprotocol.ClockClass { return c.syncState.ClockClass }

// SetHoldOverTimeout is a no-op for TBC: holdover is driven by the DPLL/ts2phc
// FSM, not a BC-style source-loss mini-holdover timer.
func (c *TBC) SetHoldOverTimeout(seconds int64) {}

// ConfigName returns the configuration name.
func (c *TBC) ConfigName() string { return c.cfgName }

// GetData returns the Data entry for the given process, creating one if needed.
func (c *TBC) GetData(processName event.EventSource) *event.Data {
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
func (c *TBC) AddEvent(ev event.Event) SyncState {
	switch ev.Source {
	case event.SYNCE:
		c.processSyncE(ev)
		return c.syncState
	case event.PMC:
		if ds, ok := ev.Data.(*event.ParentDSData); ok {
			c.updateUpstreamParentDataSet(ds.ParentDataSet)
			c.evaluateState()
		}
		return c.syncState
	default:
		d := c.GetData(ev.Source)
		d.AddEvent(ev)
		d.UpdateState()
		c.updateLeadingClockData(ev)
		c.evaluateState()
		return c.syncState
	}
}

func (c *TBC) evaluateState() {
	prevState := c.syncState.State
	prevClockClass := c.syncState.ClockClass

	needsTTSCAnnounce, needsDownstreamUpdate := c.updateState()

	profile := strings.Replace(c.cfgName, "ts2phc", "ptp4l", 1)
	if c.syncState.State != prevState {
		c.sendIPC(ipc.Message{
			Type:    ipc.TypePTPState,
			Profile: profile,
			IFace:   c.syncState.LeadingIFace,
			Values:  ipc.StateValue{State: event.PtpStateToIPCState(c.syncState.State)},
		})
	}
	if c.syncState.ClockClass != prevClockClass {
		c.sendIPC(ipc.Message{
			Type:    ipc.TypeClockClass,
			Profile: profile,
			Values:  ipc.ClockClassValue{ClockClass: uint8(c.syncState.ClockClass)},
		})
	}

	emitOverallSyncStateIfChanged(c.sendIPC, &c.overallSyncState, c.syncState.State, c.osClockState, profile)

	if needsTTSCAnnounce {
		c.announcedClockClass = c.syncState.ClockClass
		c.announcedClockAccuracy = c.syncState.ClockAccuracy
	}
	if needsDownstreamUpdate {
		c.updateDownstreamData(c.cfgName)
	}
}

func (c *TBC) status() string {
	return fmt.Sprintf("T-BC[%d]:[%s] %s offset %d T-BC-STATUS %s\n",
		c.syncState.LastLoggedTime, c.cfgName, c.syncState.LeadingIFace, c.syncState.ClockOffset, c.syncState.State)
}

// updateState updates the BC/TSC state machine using TBC's owned
// syncState and data. Returns whether a TTSC clock class announcement is
// needed and whether downstream data needs updating.
func (c *TBC) updateState() (needsTTSCAnnounce, needsDownstreamUpdate bool) {
	dpllState := event.PTP_NOTSET
	ts2phcState := event.PTP_FREERUN
	updateDownstreamData := false
	leadingInterface := c.getLeadingInterfaceBC()
	if leadingInterface == event.LEADING_INTERFACE_UNKNOWN {
		glog.Infof("Leading interface is not yet identified, clock state reporting delayed.")
		c.syncState.LeadingIFace = leadingInterface
		return false, false
	}

	c.syncState.SourceLost = false
	c.syncState.LeadingIFace = leadingInterface
	if len(c.data) > 0 {
		for _, d := range c.data {
			switch d.ProcessName {
			case event.DPLL:
				dpllState = d.State
			case event.TS2PHCProcessName:
				ts2phcState = d.State
			}
		}
	} else {
		glog.Info("initializing default ClockSyncState for ", c.cfgName)
		c.syncState.State = event.PTP_FREERUN
		c.syncState.ClockClass = protocol.ClockClassFreerun
		c.syncState.ClockAccuracy = fbprotocol.ClockAccuracyUnknown
		c.syncState.LastLoggedTime = time.Now().Unix()
		c.syncState.LeadingIFace = leadingInterface
		glog.Info(c.status())
		return false, false
	}

	isTTSC := c.leadingClockData.clockID != "" && c.leadingClockData.controlledPortsConfig == ""

	glog.V(14).Info("current BC state: ", c.syncState.State)
	switch c.syncState.State {
	case event.PTP_NOTSET, event.PTP_FREERUN:
		if !c.isSourceLostBC() && c.inSyncCondition() {
			c.syncState.State = event.PTP_LOCKED
			glog.Info("BC FSM: FREERUN to LOCKED")
			c.leadingClockData.lastInSpec = true
			updateDownstreamData = true
		}
	case event.PTP_LOCKED:
		if c.freeRunCondition() || c.hasNonLeadingDPLLFault() {
			c.syncState.State = event.PTP_FREERUN
			c.syncState.ClockClass = protocol.ClockClassFreerun
			glog.Info("BC FSM: LOCKED to FREERUN")
			updateDownstreamData = true
		} else if c.isSourceLostBC() {
			c.syncState.State = event.PTP_HOLDOVER
			c.syncState.ClockClass = fbprotocol.ClockClass(135)
			glog.Info("BC FSM: LOCKED to HOLDOVER")
			c.leadingClockData.lastInSpec = true
			updateDownstreamData = true
		} else {
			if *c.leadingClockData.upstreamTimeProperties != *c.leadingClockData.downstreamTimeProperties {
				c.leadingClockData.downstreamTimeProperties = c.leadingClockData.upstreamTimeProperties
				updateDownstreamData = true
			}
			if *c.leadingClockData.upstreamParentDataSet != *c.leadingClockData.downstreamParentDataSet {
				c.leadingClockData.downstreamParentDataSet = c.leadingClockData.upstreamParentDataSet
				updateDownstreamData = true
			}
			if c.leadingClockData.upstreamParentDataSet.GrandmasterClockClass == uint8(protocol.ClockClassFreerun) {
				updateDownstreamData = false // Don't propagate uptream free run and instead let future call move to holdover/freerun
			} else if !isTTSC {
				upstreamClass := fbprotocol.ClockClass(c.leadingClockData.upstreamParentDataSet.GrandmasterClockClass)
				upstreamAccuracy := fbprotocol.ClockAccuracy(c.leadingClockData.upstreamParentDataSet.GrandmasterClockAccuracy)
				if c.syncState.ClockClass != upstreamClass || c.syncState.ClockAccuracy != upstreamAccuracy {
					c.syncState.ClockClass = upstreamClass
					c.syncState.ClockAccuracy = upstreamAccuracy
				}
			}
		}
	case event.PTP_HOLDOVER:
		nonLeadingFault := c.hasNonLeadingDPLLFault()
		switch {
		case nonLeadingFault || c.freeRunCondition():
			c.syncState.State = event.PTP_FREERUN
			c.syncState.ClockClass = protocol.ClockClassFreerun
			glog.Info("BC FSM: HOLDOVER to FREERUN")
			updateDownstreamData = true
		case c.inSyncCondition() && !c.isSourceLostBC():
			c.syncState.State = event.PTP_LOCKED
			glog.Info("BC FSM: HOLDOVER to LOCKED")
			updateDownstreamData = true
		default:
			inSpec := false
			if c.leadingClockData.lastInSpec {
				inSpec = c.inSpecCondition()
			}
			if c.leadingClockData.lastInSpec != inSpec {
				c.leadingClockData.lastInSpec = inSpec
				if !inSpec {
					if c.syncState.ClockClass != fbprotocol.ClockClass(165) {
						c.syncState.ClockClass = fbprotocol.ClockClass(165)
						glog.Info("BC FSM: HOLDOVER sub-state Out Of Spec")
						updateDownstreamData = true
					}
				} else {
					if c.syncState.ClockClass != fbprotocol.ClockClass(135) {
						c.syncState.ClockClass = fbprotocol.ClockClass(135)
						glog.Info("BC FSM: HOLDOVER sub-state In Spec")
						updateDownstreamData = true
					}
				}
			}
		}
	}
	c.syncState.LeadingIFace = leadingInterface
	if c.syncState.State != event.PTP_LOCKED {
		c.syncState.ClockAccuracy = fbprotocol.ClockAccuracyUnknown
	}

	switch c.syncState.State {
	case event.PTP_FREERUN:
		c.syncState.ClockOffset = FaultyPhaseOffset
	case event.PTP_HOLDOVER:
		c.syncState.ClockOffset = c.getCalculatedHoldoverOffset()
	default:
		c.syncState.ClockOffset = c.getLargestOffset()
	}

	if isTTSC && c.syncState.ClockClass != fbprotocol.ClockClassSlaveOnly {
		c.syncState.ClockClass = fbprotocol.ClockClassSlaveOnly
	}
	if updateDownstreamData && c.syncState.ClockClass != protocol.ClockClassUninitialized {
		if isTTSC {
			needsTTSCAnnounce = true
		} else {
			needsDownstreamUpdate = true
		}
	}
	logTime := time.Now().Unix()
	if c.syncState.LastLoggedTime != logTime {
		glog.Info(c.status())
		c.syncState.LastLoggedTime = logTime
		glog.Infof("dpll State %s, tsphc state %s, BC state %s, BC offset %d",
			dpllState, ts2phcState, c.syncState.State, c.syncState.ClockOffset)
	}
	return needsTTSCAnnounce, needsDownstreamUpdate
}

// Reset resets the clock state.
func (c *TBC) Reset() {
	c.leadingClockData = newLeadingClockParams()
	c.syncState = SyncState{
		State:         event.PTP_NOTSET,
		ClockClass:    protocol.ClockClassUninitialized,
		ClockAccuracy: fbprotocol.ClockAccuracyUnknown,
	}
	c.overallSyncState = event.PTP_FREERUN
	c.announcedClockClass = 0
	c.announcedClockAccuracy = 0
	c.data = nil
}

func (c *TBC) updateUpstreamParentDataSet(parentDS protocol.ParentDataSet) {
	if !parentDS.Equal(c.leadingClockData.upstreamParentDataSet) {
		c.leadingClockData.upstreamParentDataSet = &parentDS
	}
}

func (c *TBC) updateDownstreamData(cfgName string) {
	if c.downstreamCancel != nil {
		c.downstreamCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.downstreamCancel = cancel

	if c.syncState.State == event.PTP_LOCKED {
		go c.downstreamAnnounceIWF(ctx, cfgName)
	} else {
		go c.announceLocalData(cfgName)
	}
}

// Implements Rec. ITU-T G.8275 (2024) Amd. 1 (08/2024)
// Table VIII.3 − T-BC-/ T-BC-P/ T-BC-A Announce message contents
// for free-run (acquiring), holdover within / out of the specification
func (c *TBC) announceLocalData(cfgName string) {
	clockID := c.leadingClockData.clockID
	controlledPortsConfig := c.leadingClockData.controlledPortsConfig
	downstreamTimeProperties := c.leadingClockData.downstreamTimeProperties
	clockClass := c.syncState.ClockClass
	clockAccuracy := c.syncState.ClockAccuracy

	egp := protocol.ExternalGrandmasterProperties{
		GrandmasterIdentity: clockID,
		StepsRemoved:        0,
	}
	glog.Infof("EGP %++v", egp)
	go func() {
		if err := c.pmcClient.SetExternalGMPropertiesNP(controlledPortsConfig, egp); err != nil {
			glog.Errorf("Failed to set external GM properties: %v", err)
		}
	}()
	c.announcedClockClass = clockClass
	c.announcedClockAccuracy = clockAccuracy
	gs := protocol.GrandmasterSettings{
		ClockQuality: fbprotocol.ClockQuality{
			ClockClass:              clockClass,
			ClockAccuracy:           fbprotocol.ClockAccuracyUnknown,
			OffsetScaledLogVariance: 0xffff,
		},
		TimePropertiesDS: protocol.TimePropertiesDS{
			TimeSource: fbprotocol.TimeSourceInternalOscillator,
		},
	}
	switch clockClass {
	case protocol.ClockClassFreerun:
		gs.TimePropertiesDS.CurrentUtcOffsetValid = false
		gs.TimePropertiesDS.Leap59 = false
		gs.TimePropertiesDS.Leap61 = false
		gs.TimePropertiesDS.PtpTimescale = true
		gs.TimePropertiesDS.TimeTraceable = false
		gs.TimePropertiesDS.FrequencyTraceable = false
		gs.TimePropertiesDS.CurrentUtcOffset = int32(c.getUtcOffset())
	case fbprotocol.ClockClass(165), fbprotocol.ClockClass(135):
		if downstreamTimeProperties == nil {
			glog.Info("Pending upstream clock data acquisition, skip updates")
			return
		}
		gs.TimePropertiesDS.CurrentUtcOffsetValid = downstreamTimeProperties.CurrentUtcOffsetValid
		gs.TimePropertiesDS.Leap59 = downstreamTimeProperties.Leap59
		gs.TimePropertiesDS.Leap61 = downstreamTimeProperties.Leap61
		gs.TimePropertiesDS.PtpTimescale = true
		if clockClass == fbprotocol.ClockClass(135) {
			gs.TimePropertiesDS.TimeTraceable = true
		} else {
			gs.TimePropertiesDS.TimeTraceable = false
		}
		gs.TimePropertiesDS.FrequencyTraceable = false
		gs.TimePropertiesDS.CurrentUtcOffset = downstreamTimeProperties.CurrentUtcOffset

	default:
	}
	go func() {
		if err := c.pmcClient.SetGMSettings(controlledPortsConfig, gs); err != nil {
			glog.Errorf("Failed to set GM settings: %v", err)
		}
	}()
	go func() {
		if err := c.pmcClient.SetGMSettings(cfgName, gs); err != nil {
			glog.Errorf("Failed to set GM settings: %v", err)
		}
	}()
}

func (c *TBC) applyIfLockedBC(context string, fn func()) bool {
	if c.syncState.State != event.PTP_LOCKED {
		glog.Infof("downstreamAnnounceIWF: BC state is %s (not LOCKED) %s, aborting", c.syncState.State, context)
		return false
	}
	fn()
	return true
}

// downstreamAnnounceIWF fetches upstream parent/time/current datasets via PMC and propagates them to the controlled
// downstream ports as GM settings.
func (c *TBC) downstreamAnnounceIWF(ctx context.Context, cfgName string) {
	ptpCfgName := strings.Replace(cfgName, "ts2phc", "ptp4l", 1)
	glog.Infof("downstreamAnnounceIWF: %s", ptpCfgName)

	controlledPortsConfig := c.leadingClockData.controlledPortsConfig

	upsteamData, fetchErr := c.pmcClient.GetParentTimeAndCurrentDS(cfgName)
	if fetchErr != nil {
		glog.Error("Failed to fetch upstream data, downstream data can not be updated.")
		return
	}

	if ctx.Err() != nil {
		glog.Info("downstreamAnnounceIWF: cancelled after PMC fetch")
		return
	}

	if !c.applyIfLockedBC("after PMC fetch", func() {
		c.leadingClockData.upstreamParentDataSet = &upsteamData.ParentDataSet
		c.leadingClockData.upstreamTimeProperties = &upsteamData.TimePropertiesDS
		c.leadingClockData.upstreamCurrentDSStepsRemoved = upsteamData.CurrentDS.StepsRemoved
	}) {
		return
	}

	if ctx.Err() != nil {
		glog.Info("downstreamAnnounceIWF: cancelled before announce")
		return
	}

	gs := protocol.GrandmasterSettings{
		ClockQuality: fbprotocol.ClockQuality{
			ClockClass:              fbprotocol.ClockClass(upsteamData.ParentDataSet.GrandmasterClockClass),
			ClockAccuracy:           fbprotocol.ClockAccuracy(upsteamData.ParentDataSet.GrandmasterClockAccuracy),
			OffsetScaledLogVariance: upsteamData.ParentDataSet.GrandmasterOffsetScaledLogVariance,
		},
		TimePropertiesDS: upsteamData.TimePropertiesDS,
	}
	es := protocol.ExternalGrandmasterProperties{
		GrandmasterIdentity: upsteamData.ParentDataSet.GrandmasterIdentity,
		StepsRemoved:        upsteamData.CurrentDS.StepsRemoved,
	}
	glog.Infof("%++v", es)
	c.announcedClockClass = gs.ClockQuality.ClockClass
	c.announcedClockAccuracy = gs.ClockQuality.ClockAccuracy

	if !c.applyIfLockedBC("before downstream PMC writes", func() {
		if err := c.pmcClient.SetExternalGMPropertiesNP(controlledPortsConfig, es); err != nil {
			glog.Error(err)
		}
	}) {
		return
	}

	if !c.applyIfLockedBC("before SetGMSettings", func() {
		if err := c.pmcClient.SetGMSettings(controlledPortsConfig, gs); err != nil {
			glog.Error(err)
		}
	}) {
		return
	}
	glog.Infof("%++v", es)

	if ctx.Err() != nil {
		glog.Info("downstreamAnnounceIWF: cancelled before downstream update")
		return
	}

	c.applyIfLockedBC("after downstream announce", func() {
		c.leadingClockData.downstreamParentDataSet = &upsteamData.ParentDataSet
		c.leadingClockData.downstreamTimeProperties = &upsteamData.TimePropertiesDS
	})
}

// hasNonLeadingDPLLFault returns true when the leading DPLL is locked but at
// least one non-leading DPLL is not locked, indicating a follower fault that
// should force the composite clock to FREERUN.
func (c *TBC) hasNonLeadingDPLLFault() bool {
	leadingInterface := c.syncState.LeadingIFace
	if leadingInterface == event.LEADING_INTERFACE_UNKNOWN {
		return false
	}
	for _, d := range c.data {
		if d.ProcessName != event.DPLL {
			continue
		}
		leadingDetail := d.GetDataDetails(leadingInterface)
		if leadingDetail == nil || leadingDetail.State != event.PTP_LOCKED {
			return false
		}
		for _, dd := range d.Details {
			if dd.IFace != leadingInterface && dd.State != event.PTP_LOCKED {
				glog.Infof("non-leading DPLL %s is %s while leading %s is locked, composite DPLL forced to FREERUN",
					dd.IFace, dd.State, leadingInterface)
				return true
			}
		}
	}
	return false
}

func (c *TBC) inSyncCondition() bool {
	if c.leadingClockData.inSyncConditionThreshold == 0 {
		glog.Info("Leading clock in-sync condition is pending initialization")
		return false
	}

	worstOffset := c.getLargestOffset()
	if math.Abs(float64(worstOffset)) < float64(c.leadingClockData.inSyncConditionThreshold) {
		c.leadingClockData.inSyncThresholdCounter++
		if c.leadingClockData.inSyncThresholdCounter >= c.leadingClockData.inSyncConditionTimes {
			return true
		}
	} else {
		c.leadingClockData.inSyncThresholdCounter = 0
	}

	glog.Info("sync condition not reached: worst offset ", worstOffset, " count ",
		c.leadingClockData.inSyncThresholdCounter, " out of ", c.leadingClockData.inSyncConditionTimes)

	return false
}

func (c *TBC) isSourceLostBC() bool {
	ptpLost := true
	dpllLost := false
	dpllLostIface := ""
	for _, d := range c.data {
		if d.ProcessName == event.PTP4l {
			for _, dd := range d.Details {
				if dd.State == event.PTP_LOCKED {
					ptpLost = false
				}
			}
		}
		if d.ProcessName == event.DPLL {
			glog.V(14).Infof("isSourceLostBC[%s]", c.cfgName)
			for _, dd := range d.Details {
				glog.V(14).Infof("  DPLL detail: iface=%s state=%s signalSource=%s sourceLost=%t offset=%d time=%d metrics=%+v", dd.IFace, dd.State, dd.SignalSource, dd.SourceLost, dd.Offset, dd.Time, dd.Metrics)
				if dd.State != event.PTP_LOCKED {
					dpllLost = true
					dpllLostIface = dd.IFace
					break
				}
			}
		}
	}
	glog.Infof("Source %s: ptpLost %t, dpllLost %t %s",
		func() string {
			if dpllLost || ptpLost {
				return "LOST"
			}
			return "NOT LOST"
		}(), ptpLost, dpllLost, dpllLostIface)
	return ptpLost || dpllLost
}

func (c *TBC) getLargestOffset() int64 {
	worstOffset := FaultyPhaseOffset
	staleTime := time.Now().UnixMilli() - StaleEventAfter
	for _, d := range c.data {
		// ts2phc offsets are not window-filtered. On T-BC the leading NIC only
		// reports ts2phc offsets in holdover, so requiring a full sliding window
		// leaves a permanently partial window that poisons inSync with FaultyPhaseOffset.
		if d.ProcessName == event.TS2PHCProcessName {
			for _, dd := range d.Details {
				if dd.Time < staleTime {
					continue
				}
				if worstOffset == FaultyPhaseOffset ||
					math.Abs(float64(dd.Offset)) > math.Abs(float64(worstOffset)) {
					worstOffset = dd.Offset
				}
			}
			continue
		}
		if d.Window.IsEmpty() {
			continue
		}
		if !d.Window.IsFull() {
			glog.Infof("Largest offset %d (window not full for %s)", FaultyPhaseOffset, d.ProcessName)
			return FaultyPhaseOffset
		}
		for _, dd := range d.Details {
			if dd.Time < staleTime {
				continue
			}
			if worstOffset == FaultyPhaseOffset {
				if dd.IFace == c.syncState.LeadingIFace {
					worstOffset = int64(d.Window.Mean())
				} else {
					worstOffset = dd.Offset
				}
			} else {
				if math.Abs(float64(dd.Offset)) > math.Abs(float64(worstOffset)) {
					worstOffset = dd.Offset
				}
			}
		}
	}
	glog.Info("Largest offset ", worstOffset)
	return worstOffset
}

func (c *TBC) getCalculatedHoldoverOffset() int64 {
	for _, d := range c.data {
		if d.ProcessName == event.DPLL {
			return int64(d.Window.LastInserted())
		}
	}
	return FaultyPhaseOffset // No DPLL entries yet return faulty
}

func (c *TBC) freeRunCondition() bool {
	if c.leadingClockData.toFreeRunThreshold == 0 {
		glog.Info("Leading clock free-run condition is pending initialization")
		return true
	}
	for _, d := range c.data {
		switch d.ProcessName {
		case event.DPLL:
			for _, dd := range d.Details {
				if dd.IFace == c.syncState.LeadingIFace {
					if math.Abs(float64(dd.Offset)) > float64(c.leadingClockData.toFreeRunThreshold) {
						glog.Infof("free-run condition on DPLL %s", dd.IFace)
						return true
					}
				}
			}
		case event.PTP4l:
			if d.Window.IsEmpty() {
				continue
			}
			ptp4lAvgOffset := int64(d.Window.Mean())
			if math.Abs(float64(ptp4lAvgOffset)) > float64(c.leadingClockData.toFreeRunThreshold) {
				glog.Infof("free-run condition on PTP4l, avg offset %d", ptp4lAvgOffset)
				return true
			}
		}
	}
	return false
}

func (c *TBC) inSpecCondition() bool {
	if c.leadingClockData.MaxInSpecOffset == 0 {
		glog.Info("Leading clock in-spec condition is pending initialization")
		return false
	}
	for _, d := range c.data {
		if d.ProcessName == event.DPLL {
			for _, dd := range d.Details {
				if dd.IFace == c.syncState.LeadingIFace {
					if math.Abs(float64(dd.Offset)) > float64(c.leadingClockData.MaxInSpecOffset) {
						glog.Infof("out-of-spec condition on DPLL ", dd.IFace)
						return false
					}
				}
			}
		}
	}
	return true
}

func (c *TBC) getLeadingInterfaceBC() string {
	if c.leadingClockData.leadingInterface != "" {
		return c.leadingClockData.leadingInterface
	}
	return event.LEADING_INTERFACE_UNKNOWN
}

// SystemClockUpdate updates the OS clock state.
func (c *TBC) SystemClockUpdate(osClockState event.PTPState) {
	c.osClockState = osClockState
	profile := strings.Replace(c.cfgName, "ts2phc", "ptp4l", 1)
	emitOverallSyncStateIfChanged(c.sendIPC, &c.overallSyncState, c.syncState.State, c.osClockState, profile)
}

func (c *TBC) processSyncE(ev event.Event) {
	ptp, ok := ev.Data.(*event.PTPData)
	if !ok || ptp == nil {
		return
	}
	profile := strings.Replace(c.cfgName, "ts2phc", "ptp4l", 1)

	eecState, hasEEC := ptp.Values[event.EEC_STATE].(string)
	if hasEEC {
		c.sendIPC(ipc.Message{
			Type:    ipc.TypeSyncEState,
			Profile: profile,
			IFace:   ev.IFace,
			Values:  ipc.SyncEStateValue{State: eecState},
		})
	}

	ql, hasQL := ptp.Values[event.QL].(byte)
	extQL, hasExtQL := ptp.Values[event.EXT_QL].(byte)
	if hasQL || hasExtQL {
		c.sendIPC(ipc.Message{
			Type:    ipc.TypeSyncEClockQuality,
			Profile: profile,
			IFace:   ev.IFace,
			Values:  ipc.SyncEClockQualityValue{QL: int(ql), ExtendedQL: int(extQL)},
		})
	}
}

func (c *TBC) updateLeadingClockData(ev event.Event) {
	ptp, ok := ev.Data.(*event.PTPData)
	if !ok {
		return
	}
	switch ev.Source {
	case event.PTP4lProcessName:
		cpc, found := ptp.Values[event.ControlledPortsConfig].(string)
		if found {
			c.leadingClockData.controlledPortsConfig = cpc
		}
		id, found := ptp.Values[event.ClockIDKey].(string)
		if found {
			c.leadingClockData.clockID = id
		}
	case event.DPLL:
		ls, found := ptp.Values[event.LeadingSource].(bool)
		if found && ls {
			c.leadingClockData.leadingInterface = ev.IFace
		}
		inSyncTh, found := ptp.Values[event.InSyncConditionThreshold].(uint64)
		if found {
			c.leadingClockData.inSyncConditionThreshold = int(inSyncTh)
		}
		inSyncTimes, found := ptp.Values[event.InSyncConditionTimes].(uint64)
		if found {
			c.leadingClockData.inSyncConditionTimes = int(inSyncTimes)
		}
		toFreeRunTh, found := ptp.Values[event.ToFreeRunThreshold].(uint64)
		if found {
			c.leadingClockData.toFreeRunThreshold = int(toFreeRunTh)
		}
		maxInSpec, found := ptp.Values[event.MaxInSpecOffset].(uint64)
		if found {
			c.leadingClockData.MaxInSpecOffset = maxInSpec
		}
	}
}
