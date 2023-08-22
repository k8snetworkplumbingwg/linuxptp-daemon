package dpll

import (
	"context"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang/glog"
	"github.com/mdlayher/genetlink"
	"github.com/openshift/linuxptp-daemon/pkg/config"
	nl "github.com/openshift/linuxptp-daemon/pkg/dpll-netlink"
	"github.com/openshift/linuxptp-daemon/pkg/event"
	"golang.org/x/sync/semaphore"
)

const (
	DPLL_UNKNOWN       = -1
	DPLL_INVALID       = 0
	DPLL_FREERUN       = 1
	DPLL_LOCKED        = 2
	DPLL_LOCKED_HO_ACQ = 3
	DPLL_HOLDOVER      = 4

	LocalMaxHoldoverOffSet = 1500  //ns
	LocalHoldoverTimeout   = 14400 //secs
	MaxInSpecOffset        = 100   //ns
	monitoringInterval     = 1 * time.Second

	LocalMaxHoldoverOffSetStr = "LocalMaxHoldoverOffSet"
	LocalHoldoverTimeoutStr   = "LocalHoldoverTimeout"
	MaxInSpecOffsetStr        = "MaxInSpecOffset"
	ClockIdStr                = "clockId"
)

type dpllApiType string

var subscribers []Subscriber

const (
	SYSFS   dpllApiType = "sysfs"
	NETLINK dpllApiType = "netlink"
	NONE    dpllApiType = "none"
)

type DependingStates struct {
	sync.Mutex
	states       map[event.EventSource]event.PTPState
	currentState event.PTPState
}

// Subscriber ... event subscriber
type Subscriber struct {
	source     event.EventSource
	dpll       *DpllConfig
	monitoring bool
	id         string
}

var dependingProcessStateMap = DependingStates{
	states: make(map[event.EventSource]event.PTPState),
}

// GetCurrentState ... get current state
func (d *DependingStates) GetCurrentState() event.PTPState {
	return d.currentState
}

func (d *DependingStates) UpdateState(source event.EventSource) {
	// do not lock here, since this function is called from other locks
	lowestState := event.PTP_FREERUN
	for _, state := range d.states {
		if state < lowestState {
			lowestState = state
		}
	}
	d.currentState = lowestState
}

// DpllConfig ... DPLL configuration
type DpllConfig struct {
	LocalMaxHoldoverOffSet uint64
	LocalHoldoverTimeout   uint64
	MaxInSpecOffset        uint64
	iface                  string
	name                   string
	slope                  float64
	timer                  int64 //secs
	inSpec                 bool
	offset                 int64
	state                  event.PTPState
	onHoldover             bool
	sourceLost             bool
	processConfig          config.ProcessConfig
	dependsOn              []event.EventSource
	exitCh                 chan struct{}
	holdoverCloseCh        chan bool
	ticker                 *time.Ticker
	apiType                dpllApiType
	// DPLL netlink connection pointer. If 'nil', use sysfs
	conn *nl.Conn
	// We need to keep latest DPLL status values, since Netlink device
	// change indications don't contain all the status fields, but
	// only the changed one(s)
	phaseStatus     int64
	frequencyStatus int64
	phaseOffset     int64
	// clockId is needed to distinguish between DPLL associated with the particular
	// iface from other DPLL units that might be present on the system. Clock ID implementation
	// is driver-specific and vendor-specific.
	clockId uint64
	sync.Mutex
	isMonitoring bool
}

// Monitor ...
func (s Subscriber) Monitor() {
	glog.Infof("Starting dpll monitoring")
	s.dpll.MonitorDpll()
}

// Topic ... event topic
func (s Subscriber) Topic() event.EventSource {
	return s.source
}

func (s Subscriber) MonitoringStarted() bool {
	return s.monitoring
}

func (s Subscriber) ID() string {
	return s.id
}

// Notify ... event notification
func (s Subscriber) Notify(source event.EventSource, state event.PTPState) {
	if s.dpll == nil || !s.dpll.isMonitoring {
		glog.Errorf("dpll subscriber %s is not initialized (monitoring state %t)", s.source, s.dpll.isMonitoring)
		return
	}
	glog.Infof("state change notification received  for %s", s.source)
	dependingProcessStateMap.Lock()
	defer dependingProcessStateMap.Unlock()
	currentState := dependingProcessStateMap.states[source]
	glog.Infof("%s notified on state change: from state %v to state %v", source, currentState, state)
	if currentState != state {
		dependingProcessStateMap.states[source] = state
		if source == event.GNSS {
			if state == event.PTP_LOCKED {
				s.dpll.sourceLost = false
			} else {
				s.dpll.sourceLost = true
			}
			glog.Infof("sourceLost %v", s.dpll.sourceLost)
		}
		s.dpll.stateDecision()
		glog.Infof("%s notified on state change: state %v", source, state)
		dependingProcessStateMap.UpdateState(source)
	} else {
		glog.Infof("ignoring state change notification for state %s", state)
	}
}

// Name ... name of the process
func (d *DpllConfig) Name() string {
	return string(event.DPLL)
}

// Stopped ... stopped
func (d *DpllConfig) Stopped() bool {
	//TODO implement me
	panic("implement me")
}

// ExitCh ... exit channel
func (d *DpllConfig) ExitCh() chan struct{} {
	return d.exitCh
}

// CmdStop ... stop command
func (d *DpllConfig) CmdStop() {
	glog.Infof("stopping %s", d.Name())
	d.ticker.Stop()
	glog.Infof("Ticker stopped %s", d.Name())
	close(d.exitCh) // terminate loop
	glog.Infof("Process %s terminated", d.Name())
}

// CmdInit ... init command
func (d *DpllConfig) CmdInit() {
	// register to event notification from other processes
	d.setAPIType()
	glog.Infof("api type %v", d.apiType)
}

// CmdRun ... run command
func (d *DpllConfig) CmdRun(stdToSocket bool) {
	// register to event notification from other processes
	subscribers = []Subscriber{}
	for _, dep := range d.dependsOn {
		dependingProcessStateMap.states[dep] = event.PTP_UNKNOWN
		// register to event notification from other processes
		s := Subscriber{source: dep, dpll: d, monitoring: false, id: string(event.DPLL)}
		subscribers = append(subscribers, s)
		event.StateRegisterer.Register(s)
	}
}

// NewDpll ... create new DPLL process
func NewDpll(clockId uint64, localMaxHoldoverOffSet, localHoldoverTimeout, maxInSpecOffset uint64,
	iface string, dependsOn []event.EventSource) *DpllConfig {
	glog.Infof("Calling NewDpll with clockId %x, localMaxHoldoverOffSet=%d, localHoldoverTimeout=%d, maxInSpecOffset=%d, iface=%s", clockId, localMaxHoldoverOffSet, localHoldoverTimeout, maxInSpecOffset, iface)
	d := &DpllConfig{
		clockId:                clockId,
		LocalMaxHoldoverOffSet: localMaxHoldoverOffSet,
		LocalHoldoverTimeout:   localHoldoverTimeout,
		MaxInSpecOffset:        maxInSpecOffset,
		slope: func() float64 {
			return (float64(localMaxHoldoverOffSet) / float64(localHoldoverTimeout)) * 1000.0
		}(),
		timer:        0,
		offset:       0,
		state:        event.PTP_FREERUN,
		iface:        iface,
		onHoldover:   false,
		sourceLost:   false,
		dependsOn:    dependsOn,
		exitCh:       make(chan struct{}),
		ticker:       time.NewTicker(monitoringInterval),
		isMonitoring: false,
	}

	d.timer = int64(math.Round(float64(d.MaxInSpecOffset*1000) / d.slope))
	glog.Infof("slope %f ps/s, offset %f ns, timer %d sec", d.slope, float64(d.MaxInSpecOffset), d.timer)
	return d
}

// nlUpdateState updates DPLL state in the DpllConfig structure.
func (d *DpllConfig) nlUpdateState(replies []*nl.DoDeviceGetReply) bool {
	valid := false
	for _, reply := range replies {
		if reply.ClockId == d.clockId {
			if reply.LockStatus == DPLL_INVALID {
				glog.Info("discarding on invalid status: ", nl.GetDpllStatusHR(reply))
				continue
			}
			glog.Info(nl.GetDpllStatusHR(reply))
			switch nl.GetDpllType(reply.Type) {
			case "eec":
				d.frequencyStatus = int64(reply.LockStatus)
				valid = true
			case "pps":
				d.phaseStatus = int64(reply.LockStatus)
				d.phaseOffset = 0 // TODO: get offset from reply when implemented
				valid = true
			}
		} else {
			glog.Info("discarding on clock ID: ", nl.GetDpllStatusHR(reply))
		}
	}
	return valid
}

// monitorNtf receives a multicast unsolicited notification and
// calls dpll state updating function.
func (d *DpllConfig) monitorNtf(c *genetlink.Conn) {
	for {
		msg, _, err := c.Receive()
		if err != nil {
			if err.Error() == "netlink receive: use of closed file" {
				glog.Info("netlink connection has been closed - stop monitoring")
			} else {
				glog.Error(err)
			}
			return
		}
		replies, err := nl.ParseDeviceReplies(msg)
		if err != nil {
			glog.Error(err)
			return
		}
		if d.nlUpdateState(replies) {
			d.stateDecision()
		}
	}
}

// checks whether sysfs file structure exists for dpll associated with the interface
func (d *DpllConfig) isSysFsPresent() bool {
	path := fmt.Sprintf("/sys/class/net/%s/device/dpll_0_state", d.iface)
	if _, err := os.Stat(path); err == nil {
		return true
	}
	return false
}

// MonitorDpllSysfs monitors DPLL through sysfs
func (d *DpllConfig) isNetLinkPresent() bool {
	conn, err := nl.Dial(nil)
	if err != nil {
		glog.Info("failed to establish dpll netlink connection: ", err)
		return false
	}
	conn.Close()
	return true
}

// setAPIType
func (d *DpllConfig) setAPIType() {
	if d.isSysFsPresent() {
		d.apiType = SYSFS
	} else if d.isNetLinkPresent() {
		d.apiType = NETLINK
	} else {
		d.apiType = NONE
	}
}

// MonitorDpllNetlink monitors DPLL through netlink
func (d *DpllConfig) MonitorDpllNetlink() {
	redial := true
	var replies []*nl.DoDeviceGetReply
	var err error
	var sem *semaphore.Weighted
	for {
		if redial {
			if d.conn == nil {
				if conn, err2 := nl.Dial(nil); err2 != nil {
					d.conn = nil
					glog.Info("failed to establish dpll netlink connection: ", err2)
					goto checkExit
				} else {
					d.conn = conn
				}
			}

			c := d.conn.GetGenetlinkConn()
			mcastId, found := d.conn.GetMcastGroupId(nl.DPLL_MCGRP_MONITOR)
			if !found {
				glog.Warning("multicast ID ", nl.DPLL_MCGRP_MONITOR, " not found")
				goto abort
			}

			replies, err = d.conn.DumpDeviceGet()
			if err != nil {
				goto abort
			}

			if d.nlUpdateState(replies) {
				d.stateDecision()
			}

			err = c.JoinGroup(mcastId)
			if err != nil {
				goto abort
			}

			sem = semaphore.NewWeighted(1)
			err = sem.Acquire(context.Background(), 1)
			if err != nil {
				goto abort
			}

			go func() {
				defer sem.Release(1)
				d.monitorNtf(c)
			}()

			goto checkExit

		abort:
			d.stopDpll()
		}

	checkExit:
		select {
		case <-d.exitCh:
			glog.Infof("terminating netlink dpll monitoring")
			select {
			case d.processConfig.EventChannel <- event.EventChannel{
				ProcessName: event.DPLL,
				IFace:       d.iface,
				CfgName:     d.processConfig.ConfigName,
				ClockType:   d.processConfig.ClockType,
				Time:        time.Now().UnixMilli(),
				Reset:       true,
			}:
			default:
				glog.Error("failed to send dpll event terminated event")
			}

			if d.onHoldover {
				close(d.holdoverCloseCh)
				d.onHoldover = false
			}

			d.stopDpll()
			return

		default:
			redial = func() bool {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
				defer cancel()

				if sem == nil {
					return false
				}

				if err = sem.Acquire(ctx, 1); err != nil {
					return false
				}

				glog.Info("dpll monitoring exited, initiating redial")
				d.stopDpll()
				return true
			}()
			time.Sleep(time.Millisecond * 250) // cpu saver
		}
	}
}

// stopDpll stops DPLL monitoring
func (d *DpllConfig) stopDpll() {
	if d.conn != nil {
		if err := d.conn.Close(); err != nil {
			glog.Errorf("error closing DPLL netlink connection: %v", err)
		}
		d.conn = nil
	}
}

// MonitorProcess is initiating monitoring of DPLL associated with a process
func (d *DpllConfig) MonitorProcess(processCfg config.ProcessConfig) {
	d.processConfig = processCfg
	// register monitoring process to be called by event
	m := Subscriber{
		source:     event.NIL,
		dpll:       d,
		monitoring: false,
		id:         string(event.DPLL),
	}
	subscribers = append(subscribers, m)
	event.StateRegisterer.Register(m)
	//d.MonitorDpll()
	// do not monitor here, the event will call monitoring when it is ready to serve
}

// MonitorDpll monitors DPLL on the discovered API, if any
func (d *DpllConfig) MonitorDpll() {
	if d.apiType == SYSFS {
		go d.MonitorDpllSysfs()
		d.isMonitoring = true
	} else if d.apiType == NETLINK {
		go d.MonitorDpllNetlink()
		d.isMonitoring = true
	} else {
		glog.Errorf("dpll monitoring is not possible, both sysfs is not available or netlink implementation is not present")
		return
	}
}

// stateDecision
func (d *DpllConfig) stateDecision() {
	dpllStatus := d.getWorseState(d.phaseStatus, d.frequencyStatus)
	glog.Infof("dpll decision: Status %d, Offset %d, In spec %v, Source lost %v, On holdover %v",
		dpllStatus, d.offset, d.inSpec, d.sourceLost, d.onHoldover)

	switch dpllStatus {
	case DPLL_FREERUN, DPLL_INVALID, DPLL_UNKNOWN:
		d.inSpec = true
		if d.onHoldover {
			d.holdoverCloseCh <- true
		}
		d.state = event.PTP_FREERUN
		glog.Infof("dpll is in FREERUN, state is FREERUN")
		d.sendDpllEvent()
	case DPLL_LOCKED:
		if !d.sourceLost && d.isOffsetInRange() {
			if d.onHoldover {
				d.holdoverCloseCh <- true
			}
			glog.Infof("dpll is locked, offset is in range, state is LOCKED")
			d.state = event.PTP_LOCKED
		} else { // what happens if source is lost and DPLL is locked? goto holdover?
			glog.Infof("dpll is locked, offset is out of range, state is FREERUN")
			d.state = event.PTP_FREERUN
		}
		d.inSpec = true
		d.sendDpllEvent()
	case DPLL_LOCKED_HO_ACQ, DPLL_HOLDOVER:
		if !d.sourceLost && d.isOffsetInRange() {
			glog.Infof("dpll is locked, source is not lost, offset is in range, state is DPLL_LOCKED_HO_ACQ or DPLL_HOLDOVER")
			if d.onHoldover {
				d.holdoverCloseCh <- true
			}
			d.inSpec = true
			d.state = event.PTP_LOCKED
		} else if d.sourceLost && d.inSpec {
			glog.Infof("dpll state is DPLL_LOCKED_HO_ACQ or DPLL_HOLDOVER,  source is lost, state is HOLDOVER")
			if !d.onHoldover {
				d.holdoverCloseCh = make(chan bool)
				go d.holdover()
			}
		} else if !d.inSpec {
			glog.Infof("dpll is not in spec ,state is DPLL_LOCKED_HO_ACQ or DPLL_HOLDOVER, offset is out of range, state is FREERUN")
			d.state = event.PTP_FREERUN
		}
		d.sendDpllEvent()
	}
}

// sendDpllEvent sends DPLL event to the event channel
func (d *DpllConfig) sendDpllEvent() {
	if d.processConfig.EventChannel == nil {
		glog.Info("Skip event - dpll is not yet initialized")
		return
	}
	eventData := event.EventChannel{
		ProcessName: event.DPLL,
		State:       d.state,
		IFace:       d.iface,
		CfgName:     d.processConfig.ConfigName,
		Values: map[event.ValueType]int64{
			event.FREQUENCY_STATUS: d.frequencyStatus,
			event.OFFSET:           d.phaseOffset,
			event.PHASE_STATUS:     d.phaseStatus,
		},
		ClockType:  d.processConfig.ClockType,
		Time:       time.Now().UnixMilli(),
		WriteToLog: true,
		Reset:      false,
	}
	select {
	case d.processConfig.EventChannel <- eventData:
		glog.Infof("dpll event sent")
	default:
		glog.Infof("failed to send dpll event, retying.")
	}
}

// MonitorDpllSysfs ... monitor dpll events
func (d *DpllConfig) MonitorDpllSysfs() {
	defer func() {
		if r := recover(); r != nil {
			glog.Warning("Recovered from panic: ", r)
			// Handle the closed channel panic if necessary
		}
	}()

	d.ticker = time.NewTicker(monitoringInterval)

	// Determine DPLL state
	d.inSpec = true

	for {
		select {
		case <-d.exitCh:
			glog.Infof("Terminating sysfs DPLL monitoring")
			d.sendDpllTerminationEvent()

			if d.onHoldover {
				close(d.holdoverCloseCh) // Cancel any holdover
			}
			return
		case <-d.ticker.C:
			// Monitor DPLL
			d.phaseStatus, d.frequencyStatus, d.phaseOffset = d.sysfs(d.iface)
			d.stateDecision()
		}
	}
}

// sendDpllTerminationEvent sends a termination event to the event channel
func (d *DpllConfig) sendDpllTerminationEvent() {
	select {
	case d.processConfig.EventChannel <- event.EventChannel{
		ProcessName: event.DPLL,
		IFace:       d.iface,
		CfgName:     d.processConfig.ConfigName,
		ClockType:   d.processConfig.ClockType,
		Time:        time.Now().UnixMilli(),
		Reset:       true,
	}:
	default:
		glog.Error("failed to send dpll terminated event")
	}

	// unregister from event notification from other processes
	if subscribers != nil {
		for _, s := range subscribers {
			s.monitoring = false
			event.StateRegisterer.Unregister(s)
		}
	}
}

// getStateQuality maps the state with relatively worse signal quality with
// a lower number for easy comparison
// Ref: ITU-T G.781 section 6.3.1 Auto selection operation
func (d *DpllConfig) getStateQuality() map[int64]float64 {
	return map[int64]float64{
		DPLL_UNKNOWN:       -1,
		DPLL_INVALID:       0,
		DPLL_FREERUN:       1,
		DPLL_HOLDOVER:      2,
		DPLL_LOCKED:        3,
		DPLL_LOCKED_HO_ACQ: 4,
	}
}

// getWorseState returns the state with worse signal quality
func (d *DpllConfig) getWorseState(pstate, fstate int64) int64 {
	sq := d.getStateQuality()
	if sq[pstate] < sq[fstate] {
		return pstate
	}
	return fstate
}

func (d *DpllConfig) holdover() {
	d.onHoldover = true
	start := time.Now()
	ticker := time.NewTicker(1 * time.Second)
	defer func() {
		ticker.Stop()
		d.onHoldover = false
		d.stateDecision()
	}()

	d.state = event.PTP_HOLDOVER
	for timeout := time.After(time.Duration(d.timer * int64(time.Second))); ; {
		select {
		case <-ticker.C:
			//calculate offset
			d.offset = int64(math.Round((d.slope / 1000) * float64(time.Since(start).Seconds())))
			glog.Infof("time since holdover start %f, offset %d nanosecond", float64(time.Since(start).Seconds()), d.offset)
			if !d.isOffsetInRange() {
				glog.Infof("offset is out of range: %v, min %v, max %v",
					d.offset, d.processConfig.GMThreshold.Min, d.processConfig.GMThreshold.Max)
				return
			}
		case <-timeout:
			d.inSpec = false
			d.state = event.PTP_FREERUN
			glog.Infof("holdover timer %d expired", d.timer)
			return
		case <-d.holdoverCloseCh:
			glog.Info("holdover was closed")
			d.inSpec = true // if someone else is closing then it should be back in spec (if it was not in spec before)
			return
		}
	}
}

func (d *DpllConfig) isOffsetInRange() bool {
	if d.offset <= d.processConfig.GMThreshold.Max && d.offset >= d.processConfig.GMThreshold.Min {
		return true
	}
	glog.Infof("dpll offset out of range: min %d, max %d, current %d",
		d.processConfig.GMThreshold.Min, d.processConfig.GMThreshold.Max, d.offset)
	return false
}

// Index of DPLL being configured [0:EEC (DPLL0), 1:PPS (DPLL1)]
// Frequency State (EEC_DPLL)
// cat /sys/class/net/interface_name/device/dpll_0_state
// Phase State
// cat /sys/class/net/ens7f0/device/dpll_1_state
// Phase Offset
// cat /sys/class/net/ens7f0/device/dpll_1_offset
func (d *DpllConfig) sysfs(iface string) (phaseState, frequencyState, phaseOffset int64) {
	if iface == "" {
		return DPLL_INVALID, DPLL_INVALID, 0
	}

	readInt64FromFile := func(path string) (int64, error) {
		content, err := os.ReadFile(path)
		if err != nil {
			return 0, err
		}
		contentStr := strings.TrimSpace(string(content))
		value, err := strconv.ParseInt(contentStr, 10, 64)
		if err != nil {
			return 0, err
		}
		return value, nil
	}

	frequencyStateStr := fmt.Sprintf("/sys/class/net/%s/device/dpll_0_state", iface)
	phaseStateStr := fmt.Sprintf("/sys/class/net/%s/device/dpll_1_state", iface)
	phaseOffsetStr := fmt.Sprintf("/sys/class/net/%s/device/dpll_1_offset", iface)

	frequencyState, err := readInt64FromFile(frequencyStateStr)
	if err != nil {
		glog.Errorf("Error reading frequency state from %s: %v", frequencyStateStr, err)
	}

	phaseState, err = readInt64FromFile(phaseStateStr)
	if err != nil {
		glog.Errorf("Error reading phase state from %s: %v", phaseStateStr, err)
	}

	phaseOffset, err = readInt64FromFile(phaseOffsetStr)
	if err != nil {
		glog.Errorf("Error reading phase offset from %s: %v", phaseOffsetStr, err)
	} else {
		phaseOffset /= 100 // Convert to nanoseconds from tens of picoseconds (divide by 100)
	}
	return phaseState, frequencyState, phaseOffset
}
