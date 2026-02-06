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
	pmcPkg "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
)

const (
	// PMCProcessName is the name identifier for PMC processes
	PMCProcessName = "pmc"
	pollTimeout    = 5 * time.Minute

	// Socket reconnect parameters
	maxReconnectAttempts = 5
	reconnectBackoffBase = 100 * time.Millisecond
	maxReconnectBackoff  = 2 * time.Second
)

// NewPMCProcess creates a new PMC process instance for monitoring PTP events.
func NewPMCProcess(runID int, eventHandler *event.EventHandler, clockType string) *PMCProcess {
	return &PMCProcess{
		configFileName:    fmt.Sprintf("ptp4l.%d.config", runID),
		messageTag:        fmt.Sprintf("[ptp4l.%d.config:{level}]", runID),
		monitorParentData: true,
		parentDSCh:        make(chan protocol.ParentDataSet, 10),
		eventHandler:      eventHandler,
		clockType:         clockType,
	}
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
	exitCh            chan struct{}
	clockType         string
	c                 net.Conn // guarded by lock
	stdToSocket       bool     // set once in CmdRun, read-only afterwards
	messageTag        string
	eventHandler      *event.EventHandler
}

// getConn returns the current socket connection under lock.
func (pmc *PMCProcess) getConn() net.Conn {
	pmc.lock.Lock()
	defer pmc.lock.Unlock()
	return pmc.c
}

// setConn sets the socket connection under lock.
func (pmc *PMCProcess) setConn(c net.Conn) {
	pmc.lock.Lock()
	defer pmc.lock.Unlock()
	pmc.c = c
}

// reconnectSocket closes the old socket connection and establishes a new one.
// It retries with exponential backoff and is responsive to shutdown signals.
// The lock is only held while modifying pmc.c to avoid blocking CmdStop().
func (pmc *PMCProcess) reconnectSocket() {
	pmc.lock.Lock()
	if !pmc.stdToSocket {
		pmc.lock.Unlock()
		return
	}
	if pmc.c != nil {
		glog.Info("Closing old socket connection due to broken pipe")
		pmc.c.Close()
		pmc.c = nil
	}
	pmc.lock.Unlock()

	glog.Info("Attempting to reconnect to event socket")

	backoff := reconnectBackoffBase
	for attempt := 1; attempt <= maxReconnectAttempts; attempt++ {
		if pmc.Stopped() {
			glog.Info("PMC process stopped, aborting reconnect attempt")
			return
		}

		newConn, err := dialSocket()
		if err == nil {
			pmc.lock.Lock()
			if pmc.c == nil {
				pmc.c = newConn
				glog.Infof("Successfully reconnected to event socket after %d attempt(s)", attempt)
			} else {
				// Another goroutine reconnected first
				newConn.Close()
				glog.Info("Socket already reconnected by another goroutine")
			}
			pmc.lock.Unlock()
			return
		}

		if attempt < maxReconnectAttempts {
			glog.Warningf("Failed to reconnect to event socket (attempt %d/%d): %v, retrying in %v",
				attempt, maxReconnectAttempts, err, backoff)
			select {
			case <-time.After(backoff):
			case <-pmc.exitCh:
				glog.Info("PMC process stopped during backoff, aborting reconnect")
				return
			}
			backoff *= 2
			if backoff > maxReconnectBackoff {
				backoff = maxReconnectBackoff
			}
		}
	}

	glog.Errorf("Failed to reconnect to event socket after %d attempts", maxReconnectAttempts)
}

// withRetryOnBrokenPipe executes fn with the current connection. If fn reports a
// broken pipe, it reconnects and retries once.
func (pmc *PMCProcess) withRetryOnBrokenPipe(fn func(net.Conn) bool) {
	if !fn(pmc.getConn()) {
		return
	}

	pmc.reconnectSocket()

	c := pmc.getConn()
	if c == nil {
		glog.Warning("Socket reconnect failed, event may not have been sent")
		return
	}
	if fn(c) {
		glog.Warning("Socket write failed again after reconnect, event may not have been sent")
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

// EmitClockClassLogs emits clock class change logs to the provided connection.
func (pmc *PMCProcess) EmitClockClassLogs(c net.Conn) {
	if c != nil {
		pmc.setConn(c)
	}
	go pmc.withRetryOnBrokenPipe(func(conn net.Conn) bool {
		return pmc.eventHandler.EmitClockClass(pmc.configFileName, conn)
	})
}

// CmdRun starts the PMC monitoring process.
func (pmc *PMCProcess) CmdRun(stdToSocket bool) {
	isStopped := pmc.getAndSetStopped(false)
	if isStopped {
		return
	}
	pmc.exitCh = make(chan struct{}, 1)
	pmc.stdToSocket = stdToSocket

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

	parentDS, err := pmcPkg.RunPMCExpGetParentDS(pmc.configFileName, false)
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

	exp, r, err := pmcPkg.GetPMCMontior(pmc.configFileName)
	if err != nil {
		if exp != nil {
			utils.CloseExpect(exp, r)
		}
		return err
	}
	defer utils.CloseExpect(exp, r)

	subscribeCmd := pmc.getMonitorSubcribeCommand()
	glog.Infof("Sending '%s' to pmc", subscribeCmd)
	exp.Send(subscribeCmd + "\n")

	workerCh := make(chan workerSignal, 5)

	go pmc.expectWorker(exp, pmc.parentDSCh, workerCh)

	for {
		select {
		case <-r:
			glog.Warningf("PMC monitoring process exited")
			return fmt.Errorf("PMC needs to restart")
		case <-pmc.exitCh:
			return nil
		case parentDS := <-pmc.parentDSCh:
			go pmc.handleParentDS(parentDS)
		case signal := <-workerCh:
			if signal.restartProcess {
				glog.Warningf("PMC process exited (%v)", signal.err)
				return fmt.Errorf("PMC needs to restart")
			}
		}
	}
}

func (pmc *PMCProcess) expectWorker(exp *expect.GExpect, parentDSCh chan<- protocol.ParentDataSet, signalCh chan<- workerSignal) {
	for {
		select {
		case <-pmc.exitCh:
			return
		default:
		}

		go pmc.Poll() // Check if anything changed while handling the last message
		_, matches, expectErr := exp.Expect(pmcPkg.GetMonitorRegex(pmc.monitorParentData), -1)

		if expectErr != nil {
			if _, ok := expectErr.(expect.TimeoutError); ok {
				continue
			} else if strings.Contains(expectErr.Error(), "EOF") || strings.Contains(expectErr.Error(), "exit") {
				signalCh <- workerSignal{err: expectErr, restartProcess: true}
				return
			}
			continue
		}

		if len(matches) > 0 && strings.Contains(matches[0], "PARENT_DATA_SET") {
			processedMessage, procErr := protocol.ProcessMessage[protocol.ParentDataSet](matches)
			if procErr != nil {
				glog.Warningf("failed to process message for PARENT_DATA_SET: %s", procErr)
			} else {
				parentDSCh <- *processedMessage
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
		pmc.withRetryOnBrokenPipe(func(c net.Conn) bool {
			return pmc.eventHandler.AnnounceClockClass(
				fbprotocol.ClockClass(parentDS.GrandmasterClockClass),
				fbprotocol.ClockAccuracy(parentDS.GrandmasterClockClass),
				pmc.configFileName, c,
				event.ClockType(pmc.clockType),
			)
		})
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
