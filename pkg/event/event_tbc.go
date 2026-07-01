package event

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"
)

const (
	// LeadingSource is a key for passing the leading source
	LeadingSource ValueType = "LeadingSource"
	// InSyncConditionThreshold is a key for passing the in-sync condition threshold
	InSyncConditionThreshold ValueType = "in-sync-th"
	// InSyncConditionTimes is a key for passing the in-sync condition counter maximum
	InSyncConditionTimes ValueType = "in-sync-times"
	// ToFreeRunThreshold is a key for passing the threshold for the to-free-run condition
	ToFreeRunThreshold ValueType = "free-run_th"
	// ControlledPortsConfig is a key for passing the controlled ports config file name
	// to the controlling instance,
	ControlledPortsConfig ValueType = "controlled-ports-config"
	// ParentDataSet is a key for passing the ParentDS
	ParentDataSet ValueType = "parent-ds"
	// CurrentDataSet is a key for passing the CurrentDS
	CurrentDataSet ValueType = "current-ds"
	// ClockIDKey is a key for passing the clock ID
	ClockIDKey ValueType = "clock-id"
	//TimePropertiesDataSet is a key for passing the TimePropertiesDS
	TimePropertiesDataSet ValueType = "time-props"
	// MaxInSpecOffset is the key for passing the MaxInSpecOffset
	MaxInSpecOffset ValueType = "max-in-spec"
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

// GetData returns the Data entry for the given process, creating one if needed.
func (c *BCClock) GetData(processName EventSource) *Data {
	for _, d := range c.data {
		if d.ProcessName == processName {
			return d
		}
	}
	d := &Data{ProcessName: processName, State: PTP_UNKNOWN, window: *utils.NewWindow(WindowSize)}
	c.data = append(c.data, d)
	return d
}

func (c *BCClock) addEvent(event Event) (Event, BCProcessResult, *DataDetails) {
	if event.Source == PTP4lProcessName {
		event.CfgName = c.cfgName
	}
	d := c.GetData(event.Source)
	d.AddEvent(event)
	d.UpdateState()
	c.updateLeadingClockData(event)

	prevState := c.syncState.state
	prevClockClass := c.syncState.clockClass

	needsTTSCAnnounce, needsDownstreamUpdate := c.updateBCState(event)

	profile := strings.Replace(c.cfgName, "ts2phc", "ptp4l", 1)
	if c.syncState.state != prevState {
		c.io.sendIPC(ipc.Message{
			Type:    ipc.TypePTPState,
			Profile: profile,
			IFace:   c.syncState.leadingIFace,
			Values:  ipc.StateValue{State: ptpStateToIPCState(c.syncState.state)},
		})
	}
	if c.syncState.clockClass != prevClockClass {
		c.io.sendIPC(ipc.Message{
			Type:    ipc.TypeClockClass,
			Profile: profile,
			Values:  ipc.ClockClassValue{ClockClass: uint8(c.syncState.clockClass)},
		})
	}

	if needsTTSCAnnounce {
		c.io.announceClockClass(c.syncState.clockClass, c.syncState.clockAccuracy, c.cfgName)
	}
	if needsDownstreamUpdate {
		c.io.updateDownstreamData(c, c.cfgName)
	}
	return event, BCProcessResult{ClockState: c.syncState}, d.GetDataDetails(event.IFace)
}

// updateBCState updates the BC/TSC state machine using BCClock's owned
// syncState and data. Returns whether a TTSC clock class announcement is
// needed and whether downstream data needs updating.
func (c *BCClock) updateBCState(event Event) (needsTTSCAnnounce, needsDownstreamUpdate bool) {
	cfgName := event.CfgName
	dpllState := PTP_NOTSET
	ts2phcState := PTP_FREERUN
	updateDownstreamData := false
	if event.Source == PTP4lProcessName {
		glog.Infof("PTP4l event: %+v", event)
	}
	leadingInterface := c.getLeadingInterfaceBC()
	if leadingInterface == LEADING_INTERFACE_UNKNOWN {
		glog.Infof("Leading interface is not yet identified, clock state reporting delayed.")
		c.syncState.leadingIFace = leadingInterface
		return false, false
	}

	c.syncState.sourceLost = false
	c.syncState.leadingIFace = leadingInterface
	if len(c.data) > 0 {
		for _, d := range c.data {
			switch d.ProcessName {
			case DPLL:
				dpllState = d.State
			case TS2PHCProcessName:
				ts2phcState = d.State
			}
		}
	} else {
		glog.Info("initializing default clkSyncState for ", cfgName)
		c.syncState.state = PTP_FREERUN
		c.syncState.clockClass = protocol.ClockClassFreerun
		c.syncState.clockAccuracy = fbprotocol.ClockAccuracyUnknown
		c.syncState.lastLoggedTime = time.Now().Unix()
		c.syncState.leadingIFace = leadingInterface
		c.syncState.clkLog = fmt.Sprintf("T-BC[%d]:[%s] %s offset %d T-BC-STATUS %s\n",
			c.syncState.lastLoggedTime, cfgName, leadingInterface, c.syncState.clockOffset,
			c.syncState.state)
		return false, false
	}

	isTTSC := (c.leadingClockData.clockID != "" && c.leadingClockData.controlledPortsConfig == "")

	glog.Info("current BC state: ", c.syncState.state)
	switch c.syncState.state {
	case PTP_NOTSET, PTP_FREERUN:
		if !c.isSourceLostBC() && c.inSyncCondition() {
			c.syncState.state = PTP_LOCKED
			glog.Info("BC FSM: FREERUN to LOCKED")
			c.leadingClockData.lastInSpec = true
			updateDownstreamData = true
		}
	case PTP_LOCKED:
		if c.freeRunCondition() || c.hasNonLeadingDPLLFault() {
			c.syncState.state = PTP_FREERUN
			c.syncState.clockClass = protocol.ClockClassFreerun
			glog.Info("BC FSM: LOCKED to FREERUN")
			updateDownstreamData = true
		} else if c.isSourceLostBC() {
			c.syncState.state = PTP_HOLDOVER
			c.syncState.clockClass = fbprotocol.ClockClass(135)
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
				if c.syncState.clockClass != upstreamClass || c.syncState.clockAccuracy != upstreamAccuracy {
					c.syncState.clockClass = upstreamClass
					c.syncState.clockAccuracy = upstreamAccuracy
				}
			}
		}
	case PTP_HOLDOVER:
		nonLeadingFault := c.hasNonLeadingDPLLFault()
		switch {
		case nonLeadingFault || c.freeRunCondition():
			c.syncState.state = PTP_FREERUN
			c.syncState.clockClass = protocol.ClockClassFreerun
			glog.Info("BC FSM: HOLDOVER to FREERUN")
			updateDownstreamData = true
		case c.inSyncCondition() && !c.isSourceLostBC():
			c.syncState.state = PTP_LOCKED
			glog.Info("BC FSM: HOLDOVER to LOCKED")
			updateDownstreamData = true
		default:
			if event.IFace == leadingInterface {
				inSpec := false
				if c.leadingClockData.lastInSpec {
					inSpec = c.inSpecCondition()
				}
				if c.leadingClockData.lastInSpec != inSpec {
					c.leadingClockData.lastInSpec = inSpec
					if !inSpec {
						if c.syncState.clockClass != fbprotocol.ClockClass(165) {
							c.syncState.clockClass = fbprotocol.ClockClass(165)
							glog.Info("BC FSM: HOLDOVER sub-state Out Of Spec")
							updateDownstreamData = true
						}
					} else {
						if c.syncState.clockClass != fbprotocol.ClockClass(135) {
							c.syncState.clockClass = fbprotocol.ClockClass(135)
							glog.Info("BC FSM: HOLDOVER sub-state In Spec")
							updateDownstreamData = true
						}
					}
				}
			}
		}
	}
	c.syncState.leadingIFace = leadingInterface
	if c.syncState.state != PTP_LOCKED {
		c.syncState.clockAccuracy = fbprotocol.ClockAccuracyUnknown
	}

	switch c.syncState.state {
	case PTP_FREERUN:
		c.syncState.clockOffset = FaultyPhaseOffset
	case PTP_HOLDOVER:
		c.syncState.clockOffset = c.getCalculatedHoldoverOffset()
	default:
		c.syncState.clockOffset = c.getLargestOffset()
	}

	if isTTSC && c.syncState.clockClass != fbprotocol.ClockClassSlaveOnly {
		c.syncState.clockClass = fbprotocol.ClockClassSlaveOnly
	}
	if updateDownstreamData && c.syncState.clockClass != protocol.ClockClassUninitialized {
		if isTTSC {
			needsTTSCAnnounce = true
		} else {
			needsDownstreamUpdate = true
		}
	}
	logTime := time.Now().Unix()
	if c.syncState.lastLoggedTime != logTime {
		c.syncState.clkLog = fmt.Sprintf("T-BC[%d]:[%s] %s offset %d T-BC-STATUS %s\n",
			logTime, cfgName, leadingInterface, c.syncState.clockOffset, c.syncState.state)
		c.syncState.lastLoggedTime = logTime
		glog.Infof("dpll State %s, tsphc state %s, BC state %s, BC offset %d",
			dpllState, ts2phcState, c.syncState.state, c.syncState.clockOffset)
	}
	return needsTTSCAnnounce, needsDownstreamUpdate
}

// UpdateUpstreamParentDataSet updates the upstream parent data set
// for the leading clock when changes are detected.
func (c *BCClock) UpdateUpstreamParentDataSet(parentDS protocol.ParentDataSet) {
	c.io.Lock()
	defer c.io.Unlock()
	if !parentDS.Equal(c.leadingClockData.upstreamParentDataSet) {
		c.leadingClockData.upstreamParentDataSet = &parentDS
	}
}

func (c *BCClock) updateDownstreamData(cfgName string) {
	if c.downstreamCancel != nil {
		c.downstreamCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.downstreamCancel = cancel

	if c.syncState.state == PTP_LOCKED {
		go c.downstreamAnnounceIWF(ctx, cfgName)
	} else {
		go c.announceLocalData(cfgName)
	}
}

// EmitClockClass re-emits the current clock class to the socket for replay.
func (c *BCClock) EmitClockClass(cfgName string) {
	clockClass, ok := c.io.getStoredClockClass(cfgName)
	if !ok {
		return
	}
	c.io.emitClockClass(clockClass, cfgName)
}

// Implements Rec. ITU-T G.8275 (2024) Amd. 1 (08/2024)
// Table VIII.3 − T-BC-/ T-BC-P/ T-BC-A Announce message contents
// for free-run (acquiring), holdover within / out of the specification
func (c *BCClock) announceLocalData(cfgName string) {
	c.io.Lock()
	clockID := c.leadingClockData.clockID
	controlledPortsConfig := c.leadingClockData.controlledPortsConfig
	downstreamTimeProperties := c.leadingClockData.downstreamTimeProperties
	clockClass := c.syncState.clockClass
	clockAccuracy := c.syncState.clockAccuracy
	c.io.Unlock()

	egp := protocol.ExternalGrandmasterProperties{
		GrandmasterIdentity: clockID,
		StepsRemoved:        0,
	}
	glog.Infof("EGP %++v", egp)
	go func() {
		if err := pmc.SetExternalGMPropertiesNP(controlledPortsConfig, egp); err != nil {
			glog.Errorf("Failed to set external GM properties: %v", err)
		}
	}()
	c.io.announceClockClass(clockClass, clockAccuracy, cfgName)
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
		// TODO: get the real freq traceability status when implemented
		gs.TimePropertiesDS.FrequencyTraceable = false
		gs.TimePropertiesDS.CurrentUtcOffset = int32(leap.GetUtcOffset())
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
		// TODO: get the real freq traceability status when implemented
		gs.TimePropertiesDS.FrequencyTraceable = false
		gs.TimePropertiesDS.CurrentUtcOffset = downstreamTimeProperties.CurrentUtcOffset

	default:
	}
	go func() {
		if err := pmc.SetGMSettings(controlledPortsConfig, gs); err != nil {
			glog.Errorf("Failed to set GM settings: %v", err)
		}
	}()
	go func() {
		if err := pmc.SetGMSettings(cfgName, gs); err != nil {
			glog.Errorf("Failed to set GM settings: %v", err)
		}
	}()
}

func (c *BCClock) applyIfLockedBC(context string, fn func()) bool {
	c.io.Lock()
	defer c.io.Unlock()
	if c.syncState.state != PTP_LOCKED {
		glog.Infof("downstreamAnnounceIWF: BC state is %s (not LOCKED) %s, aborting", c.syncState.state, context)
		return false
	}
	fn()
	return true
}

// downstreamAnnounceIWF fetches upstream parent/time/current datasets via PMC and propagates them to the controlled
// downstream ports as GM settings.
func (c *BCClock) downstreamAnnounceIWF(ctx context.Context, cfgName string) {
	ptpCfgName := strings.Replace(cfgName, "ts2phc", "ptp4l", 1)
	glog.Infof("downstreamAnnounceIWF: %s", ptpCfgName)

	c.io.Lock()
	controlledPortsConfig := c.leadingClockData.controlledPortsConfig
	c.io.Unlock()

	upsteamData, fetchErr := pmc.GetParentTimeAndCurrentDS(cfgName)
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
	c.io.announceClockClass(gs.ClockQuality.ClockClass, gs.ClockQuality.ClockAccuracy, cfgName)
	if err := pmc.SetExternalGMPropertiesNP(controlledPortsConfig, es); err != nil {
		glog.Error(err)
	}
	if err := pmc.SetGMSettings(controlledPortsConfig, gs); err != nil {
		glog.Error(err)
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
func (c *BCClock) hasNonLeadingDPLLFault() bool {
	leadingInterface := c.syncState.leadingIFace
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

func (c *BCClock) inSyncCondition() bool {
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

func (c *BCClock) isSourceLostBC() bool {
	ptpLost := true
	dpllLost := false
	dpllLostIface := ""
	for _, d := range c.data {
		if d.ProcessName == PTP4l {
			for _, dd := range d.Details {
				if dd.State == PTP_LOCKED {
					ptpLost = false
				}
			}
		}
		if d.ProcessName == DPLL {
			for _, dd := range d.Details {
				if dd.State != PTP_LOCKED {
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

func (c *BCClock) getLargestOffset() int64 {
	worstOffset := FaultyPhaseOffset
	staleTime := time.Now().UnixMilli() - StaleEventAfter
	for _, d := range c.data {
		if d.window.IsEmpty() {
			continue
		}
		if !d.window.IsFull() {
			glog.Infof("Largest offset %d (window not full for %s)", FaultyPhaseOffset, d.ProcessName)
			return FaultyPhaseOffset
		}
		for _, dd := range d.Details {
			if dd.time < staleTime {
				continue
			}
			if worstOffset == FaultyPhaseOffset {
				if dd.IFace == c.syncState.leadingIFace {
					worstOffset = int64(d.window.Mean())
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

func (c *BCClock) getCalculatedHoldoverOffset() int64 {
	for _, d := range c.data {
		if d.ProcessName == DPLL {
			return int64(d.window.LastInserted())
		}
	}
	return FaultyPhaseOffset // No DPLL entries yet return faulty
}

func (c *BCClock) freeRunCondition() bool {
	if c.leadingClockData.toFreeRunThreshold == 0 {
		glog.Info("Leading clock free-run condition is pending initialization")
		return true
	}
	for _, d := range c.data {
		switch d.ProcessName {
		case DPLL:
			for _, dd := range d.Details {
				if dd.IFace == c.syncState.leadingIFace {
					if math.Abs(float64(dd.Offset)) > float64(c.leadingClockData.toFreeRunThreshold) {
						glog.Infof("free-run condition on DPLL %s", dd.IFace)
						return true
					}
				}
			}
		case PTP4l:
			if d.window.IsEmpty() {
				continue
			}
			ptp4lAvgOffset := int64(d.window.Mean())
			if math.Abs(float64(ptp4lAvgOffset)) > float64(c.leadingClockData.toFreeRunThreshold) {
				glog.Infof("free-run condition on PTP4l, avg offset %d", ptp4lAvgOffset)
				return true
			}
		}
	}
	return false
}

func (c *BCClock) inSpecCondition() bool {
	if c.leadingClockData.MaxInSpecOffset == 0 {
		glog.Info("Leading clock in-spec condition is pending initialization")
		return false
	}
	for _, d := range c.data {
		if d.ProcessName == DPLL {
			for _, dd := range d.Details {
				if dd.IFace == c.syncState.leadingIFace {
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

func (c *BCClock) getLeadingInterfaceBC() string {
	if c.leadingClockData.leadingInterface != "" {
		return c.leadingClockData.leadingInterface
	}
	return LEADING_INTERFACE_UNKNOWN
}

func worstOfState(a, b PTPState) PTPState {
	if a == PTP_FREERUN || b == PTP_FREERUN {
		return PTP_FREERUN
	}
	if a == PTP_HOLDOVER || b == PTP_HOLDOVER {
		return PTP_HOLDOVER
	}
	return PTP_LOCKED
}

func (c *BCClock) updateOverallSyncState(osClockState PTPState) bool {
	prev := c.overallSyncState
	c.overallSyncState = worstOfState(c.syncState.state, osClockState)
	return c.overallSyncState != prev
}

func ptpStateToIPCState(s PTPState) string {
	switch s {
	case PTP_LOCKED:
		return ipc.StateLocked
	case PTP_HOLDOVER:
		return ipc.StateHoldover
	default:
		return ipc.StateFreerun
	}
}

func (c *BCClock) processSyncE(event Event) {
	ptp, ok := event.Data.(*PTPData)
	if !ok || ptp == nil {
		return
	}
	profile := strings.Replace(c.cfgName, "ts2phc", "ptp4l", 1)

	eecState, hasEEC := ptp.Values[EEC_STATE].(string)
	if hasEEC {
		c.io.sendIPC(ipc.Message{
			Type:    ipc.TypeSyncEState,
			Profile: profile,
			IFace:   event.IFace,
			Values:  ipc.SyncEStateValue{State: eecState},
		})
	}

	ql, hasQL := ptp.Values[QL].(byte)
	extQL, hasExtQL := ptp.Values[EXT_QL].(byte)
	if hasQL || hasExtQL {
		c.io.sendIPC(ipc.Message{
			Type:    ipc.TypeSyncEClockQuality,
			Profile: profile,
			IFace:   event.IFace,
			Values:  ipc.SyncEClockQualityValue{QL: int(ql), ExtendedQL: int(extQL)},
		})
	}
}

func (c *BCClock) updateLeadingClockData(event Event) {
	ptp, ok := event.Data.(*PTPData)
	if !ok {
		return
	}
	switch event.Source {
	case PTP4lProcessName:
		cpc, found := ptp.Values[ControlledPortsConfig].(string)
		if found {
			c.leadingClockData.controlledPortsConfig = cpc
		}
		id, found := ptp.Values[ClockIDKey].(string)
		if found {
			c.leadingClockData.clockID = id
		}
	case DPLL:
		ls, found := ptp.Values[LeadingSource].(bool)
		if found && ls {
			c.leadingClockData.leadingInterface = event.IFace
		}
		inSyncTh, found := ptp.Values[InSyncConditionThreshold].(uint64)
		if found {
			c.leadingClockData.inSyncConditionThreshold = int(inSyncTh)
		}
		inSyncTimes, found := ptp.Values[InSyncConditionTimes].(uint64)
		if found {
			c.leadingClockData.inSyncConditionTimes = int(inSyncTimes)
		}
		toFreeRunTh, found := ptp.Values[ToFreeRunThreshold].(uint64)
		if found {
			c.leadingClockData.toFreeRunThreshold = int(toFreeRunTh)
		}
		maxInSpec, found := ptp.Values[MaxInSpecOffset].(uint64)
		if found {
			c.leadingClockData.MaxInSpecOffset = maxInSpec
		}
	}
}
