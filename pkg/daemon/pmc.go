package daemon

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/golang/glog"
	expect "github.com/google/goexpect"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	pmcPkg "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
)

const (
	// PMCProcessName is the name identifier for PMC processes
	PMCProcessName = "pmc"
	pollTimeout    = 5 * time.Minute
)

// PortStateChangeCallback is called by PMCProcess when a port-state
// notification is received and the role has actually changed.
type PortStateChangeCallback func(iface string, role, previousRole constants.PTPPortRole)

// PMCPortStateOpts groups the optional port-state monitoring parameters
// for NewPMCProcess. When nil / zero-value, port-state monitoring is disabled.
type PMCPortStateOpts struct {
	PortIfaceMap map[int]string
	OnChange     PortStateChangeCallback
}

// NewPMCProcess creates a new PMC process instance for monitoring PTP events.
func NewPMCProcess(runID int, eventHandler *event.EventHandler, clockType string, portStateOpts *PMCPortStateOpts) *PMCProcess {
	p := &PMCProcess{
		configFileName:    fmt.Sprintf("ptp4l.%d.config", runID),
		messageTag:        fmt.Sprintf("[ptp4l.%d.config:{level}]", runID),
		monitorParentData: true,
		parentDSCh:        make(chan protocol.ParentDataSet, 10),
		eventHandler:      eventHandler,
		clockType:         clockType,
		getMonitorFn:      pmcPkg.GetPMCMontior,
	}
	if portStateOpts != nil && portStateOpts.OnChange != nil && len(portStateOpts.PortIfaceMap) > 0 {
		p.monitorPortState = true
		p.portDSCh = make(chan protocol.PortDataSet, 10)
		p.portIfaceMap = portStateOpts.PortIfaceMap
		p.lastPortRole = make(map[string]constants.PTPPortRole, len(portStateOpts.PortIfaceMap))
		p.onPortStateChange = portStateOpts.OnChange
	}
	return p
}

// PMCProcess manages a PMC (PTP Management Client) process for monitoring PTP events.
type PMCProcess struct {
	lock              sync.Mutex
	configFileName    string
	stopped           bool
	monitorPortState  bool
	monitorTimeSync   bool
	monitorParentData bool
	monitorCMLDS      bool
	parentDS          *protocol.ParentDataSet
	parentDSCh        chan protocol.ParentDataSet
	portDSCh          chan protocol.PortDataSet
	exitCh            chan struct{}
	clockType         string
	c                 net.Conn // guarded by lock
	messageTag        string
	eventHandler      *event.EventHandler

	// Port-state monitoring fields (non-nil only when monitorPortState is true).
	portIfaceMap      map[int]string
	lastPortRole      map[string]constants.PTPPortRole
	onPortStateChange PortStateChangeCallback

	getMonitorFn func(string) (*expect.GExpect, <-chan error, error)
}

// getConn returns the current socket connection under lock.
func (pmc *PMCProcess) getConn() net.Conn {
	pmc.lock.Lock()
	defer pmc.lock.Unlock()
	return pmc.c
}

// setConn sets the socket connection under lock, closing the previous one if it exists.
func (pmc *PMCProcess) setConn(c net.Conn) {
	pmc.lock.Lock()
	oldConn := pmc.c
	pmc.c = c
	pmc.lock.Unlock()
	if oldConn != nil && oldConn != c {
		if err := oldConn.Close(); err != nil {
			glog.Warningf("failed to close old pmc connection: %v", err)
		}
	}
}

// Name returns the process name.
func (pmc *PMCProcess) Name() string {
	return PMCProcessName
}

// Stopped returns whether the process has been stopped.
func (pmc *PMCProcess) Stopped() bool {
	pmc.lock.Lock()
	defer pmc.lock.Unlock()
	return pmc.stopped
}

func (pmc *PMCProcess) getAndSetStopped(val bool) bool {
	pmc.lock.Lock()
	defer pmc.lock.Unlock()
	oldVal := pmc.stopped
	pmc.stopped = val
	return oldVal
}

// CmdStop signals the process to stop.
func (pmc *PMCProcess) CmdStop() {
	pmc.getAndSetStopped(true)
	select {
	case <-pmc.exitCh:
	default:
		close(pmc.exitCh)
	}
}

// CmdInit initializes the process state.
func (pmc *PMCProcess) CmdInit() {
}

// ProcessStatus processes status updates for the PMC process.
func (pmc *PMCProcess) ProcessStatus(c net.Conn, status int64) {
	if c != nil {
		pmc.setConn(c)
	}
	processStatus(pmc.getConn(), PMCProcessName, pmc.messageTag, status)
}

func btof(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func (pmc *PMCProcess) getMonitorSubcribeCommand() string {
	return fmt.Sprintf(
		"SET SUBSCRIBE_EVENTS_NP duration -1 "+
			"NOTIFY_PORT_STATE %s "+
			"NOTIFY_TIME_SYNC %s "+
			"NOTIFY_PARENT_DATA_SET %s "+
			"NOTIFY_CMLDS %s",
		btof(pmc.monitorPortState),
		btof(pmc.monitorTimeSync),
		btof(pmc.monitorParentData),
		btof(pmc.monitorCMLDS),
	)
}

// EmitClockClassLogs emits clock class change logs via the EventHandler's connection.
func (pmc *PMCProcess) EmitClockClassLogs() {
	go pmc.eventHandler.EmitClockClass(pmc.configFileName)
}

// CmdRun starts the PMC monitoring process.
func (pmc *PMCProcess) CmdRun(stdToSocket bool) {
	isStopped := pmc.getAndSetStopped(false)
	if isStopped {
		return
	}
	pmc.exitCh = make(chan struct{}, 1)

	go func() {
		for {
			if pmc.Stopped() {
				return
			}

			var c net.Conn
			if stdToSocket {
				cAttempt, dialErr := dialSocket()
				for dialErr != nil {
					cAttempt, dialErr = dialSocket()
				}
				c = cAttempt
			}
			monitorErr := pmc.Monitor(c)
			if monitorErr == nil && pmc.Stopped() {
				return
			}
		}
	}()
}

// workerSignal represents a signal from the expectWorker to the main monitor loop
type workerSignal struct {
	err            error
	restartProcess bool
}

// Poll runs a Poll operation in a goroutine and sends the result to the struct's ParentDataSet channel
func (pmc *PMCProcess) Poll() {
	select {
	case <-pmc.exitCh:
		return
	default:
	}

	parentDS, err := pmcPkg.GetParentDS(pmc.configFileName)
	if err != nil {
		glog.Error("pmc poll failure ", err)
		return
	}

	pmc.parentDSCh <- parentDS
}

func (pmc *PMCProcess) monitor(conn net.Conn) error {
	if conn != nil {
		pmc.setConn(conn)
	}

	exp, r, err := pmc.getMonitorFn(pmc.configFileName)
	if err != nil {
		if exp != nil {
			utils.CloseExpect(exp, r)
		}
		return err
	}

	doneCh := make(chan struct{})
	defer func() {
		close(doneCh)
		utils.CloseExpect(exp, r)
	}()

	subscribeCmd := pmc.getMonitorSubcribeCommand()
	glog.Infof("Sending '%s' to pmc", subscribeCmd)
	exp.Send(subscribeCmd + "\n")

	workerCh := make(chan workerSignal, 5)

	go pmc.expectWorker(exp, pmc.parentDSCh, workerCh, doneCh)

	portDSCh := pmc.portDSCh
	if portDSCh == nil {
		portDSCh = make(chan protocol.PortDataSet) // never fires
	}

	for {
		select {
		case <-r:
			glog.Warningf("PMC monitoring process exited")
			return fmt.Errorf("PMC needs to restart")
		case <-pmc.exitCh:
			return nil
		case parentDS := <-pmc.parentDSCh:
			go pmc.handleParentDS(parentDS)
		case portDS := <-portDSCh:
			go pmc.handlePortDS(portDS)
		case signal := <-workerCh:
			if signal.restartProcess {
				glog.Warningf("PMC process exited (%v)", signal.err)
				return fmt.Errorf("PMC needs to restart")
			}
		}
	}
}

func (pmc *PMCProcess) expectWorker(exp *expect.GExpect, parentDSCh chan<- protocol.ParentDataSet, signalCh chan<- workerSignal, doneCh <-chan struct{}) {
	for {
		select {
		case <-pmc.exitCh:
			return
		case <-doneCh:
			return
		default:
		}

		go pmc.Poll() // Check if anything changed while handling the last message
		_, matches, expectErr := exp.Expect(pmcPkg.GetMonitorRegex(pmc.monitorParentData, pmc.monitorPortState), -1)

		if expectErr != nil {
			if _, ok := expectErr.(expect.TimeoutError); ok {
				continue
			} else if strings.Contains(expectErr.Error(), "EOF") || strings.Contains(expectErr.Error(), "exit") {
				signalCh <- workerSignal{err: expectErr, restartProcess: true}
				return
			}
			glog.Warningf("expectWorker: unexpected error from Expect: %v", expectErr)
			continue
		}

		if len(matches) == 0 {
			continue
		}

		// matches comes from the combined parent|port alternation regex, so its
		// capture-group positions include every group from both branches, with
		// the non-participating branch's groups present as empty strings. Re-run
		// the DataSet-specific regex against the matched text so submatch
		// indices line up with that DataSet's own Keys() again.
		header := matches[0]
		switch {
		case strings.Contains(header, "PARENT_DATA_SET"):
			parentMatches := pmcPkg.ParentDataSetRegExp().FindStringSubmatch(header)
			processedMessage, procErr := protocol.ProcessMessage[protocol.ParentDataSet](parentMatches)
			if procErr != nil {
				glog.Warningf("failed to process message for PARENT_DATA_SET: %s", procErr)
			} else {
				parentDSCh <- *processedMessage
			}

		case pmc.monitorPortState && pmc.portDSCh != nil &&
			strings.Contains(header, "PORT_DATA_SET "):
			portMatches := pmcPkg.PortDataSetRegExp().FindStringSubmatch(header)
			processedMessage, procErr := protocol.ProcessMessage[protocol.PortDataSet](portMatches)
			if procErr != nil {
				glog.Warningf("failed to process message for PORT_DATA_SET: %s", procErr)
			} else {
				pmc.portDSCh <- *processedMessage
			}
		}
	}
}

func (pmc *PMCProcess) handleParentDS(parentDS protocol.ParentDataSet) {
	if pmc.parentDS != nil && pmc.parentDS.Equal(&parentDS) {
		glog.Infof("ParentDataSet unchanged, skipping processing for %s", pmc.configFileName)
		return
	}

	glog.Info(parentDS.String())
	oldParentDS := pmc.parentDS
	pmc.parentDS = &parentDS

	if pmc.clockType == TBC {
		pmc.eventHandler.UpdateUpstreamParentDataSet(parentDS)
	} else if oldParentDS == nil || oldParentDS.GrandmasterClockClass != parentDS.GrandmasterClockClass {
		pmc.eventHandler.AnnounceClockClass(
			fbprotocol.ClockClass(parentDS.GrandmasterClockClass),
			fbprotocol.ClockAccuracy(parentDS.GrandmasterClockAccuracy),
			pmc.configFileName,
			event.ClockType(pmc.clockType),
		)
	}
}

func (pmc *PMCProcess) handlePortDS(portDS protocol.PortDataSet) {
	portNum, err := portDS.PortNumber()
	if err != nil {
		glog.Warningf("PMC port-state: cannot extract port number: %v", err)
		return
	}

	iface, ok := pmc.portIfaceMap[portNum]
	if !ok {
		glog.Warningf("PMC port-state: unknown port number %d (identity=%s)", portNum, portDS.PortIdentity)
		return
	}

	role, _ := parser.PortStateToRole(portDS.PortState)
	previousRole := pmc.lastPortRole[iface]
	pmc.lastPortRole[iface] = role

	if role == previousRole {
		return
	}

	glog.Infof("PMC port-state: iface=%s state=%s role=%v previousRole=%v", iface, portDS.PortState, role, previousRole)

	if pmc.onPortStateChange != nil {
		pmc.onPortStateChange(iface, role, previousRole)
	}
}

// Monitor continuously monitors the PMC process and handles restarts.
func (pmc *PMCProcess) Monitor(c net.Conn) error {
	for {
		err := pmc.monitor(c)
		if err != nil {
			select {
			case <-pmc.exitCh:
				glog.Info("PMC Monitor stopping gracefully")
				return nil
			default:
				glog.Info("pmc process hit an issue (%s). restarting...", err)
				continue
			}
		}
		return err
	}
}

// ExitCh returns the exit channel for the process.
func (pmc *PMCProcess) ExitCh() chan struct{} {
	return pmc.exitCh
}

// MonitorProcess is a placeholder for process monitoring configuration.
func (pmc *PMCProcess) MonitorProcess(_ config.ProcessConfig) {
}
