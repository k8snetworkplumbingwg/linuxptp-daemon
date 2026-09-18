package daemon

import (
	"fmt"
	"strings"
	"sync"
	"time"

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
	// defaultMonitorPollInterval bounds how long the monitor loop will block
	// waiting for a NOTIFY_PARENT_DATA_SET push before it re-polls the parent
	// data set on its own. It is a fallback for callers (e.g. tests) that do not
	// supply an interval.
	defaultMonitorPollInterval = 30 * time.Second
)

// NewPMCProcess creates a new PMC process instance for monitoring PTP events.
func NewPMCProcess(runID int, eventCh chan<- event.Event, clockType string, pollInterval time.Duration) *PMCProcess {
	return &PMCProcess{
		configFileName:    fmt.Sprintf("ptp4l.%d.config", runID),
		messageTag:        fmt.Sprintf("[ptp4l.%d.config:{level}]", runID),
		monitorParentData: true,
		parentDSCh:        make(chan protocol.ParentDataSet, 10),
		eventCh:           eventCh,
		clockType:         clockType,
		pollInterval:      pollInterval,
		getMonitorFn:      pmcPkg.GetPMCMontior,
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
	// pollInterval bounds how long expectWorker blocks on Expect before it
	// re-polls the parent data set. This guarantees the clock class metric
	// recovers within one interval even if a NOTIFY_PARENT_DATA_SET push is
	// never delivered (observed on netdevsim after a link outage recovers).
	pollInterval time.Duration
	messageTag   string
	eventCh      chan<- event.Event

	getMonitorFn func(string) (*expect.GExpect, <-chan error, error)
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
func (pmc *PMCProcess) ProcessStatus(status int64) {
	processStatus(PMCProcessName, pmc.messageTag, status)
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

// CmdRun starts the PMC monitoring process.
func (pmc *PMCProcess) CmdRun() {
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

			monitorErr := pmc.Monitor()
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

func (pmc *PMCProcess) monitor() error {
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

func (pmc *PMCProcess) expectWorker(exp *expect.GExpect, parentDSCh chan<- protocol.ParentDataSet, signalCh chan<- workerSignal, doneCh <-chan struct{}) {
	// Bound the Expect wait so the loop re-polls every pollInterval even when no
	// NOTIFY_PARENT_DATA_SET push arrives. Without this the loop blocks for the
	// spawn timeout (~10m), so a parent-data-set change that is not pushed (e.g.
	// clock class returning to 6 after a link outage on netdevsim) is not seen
	// until that timeout expires — long enough to fail recovery tests.
	pollInterval := pmc.pollInterval
	if pollInterval <= 0 {
		pollInterval = defaultMonitorPollInterval
	}

	for {
		select {
		case <-pmc.exitCh:
			return
		case <-doneCh:
			return
		default:
		}

		go pmc.Poll() // Check if anything changed while handling the last message
		_, matches, expectErr := exp.Expect(pmcPkg.GetMonitorRegex(pmc.monitorParentData), pollInterval)

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
		glog.V(14).Infof("ParentDataSet unchanged, skipping processing for %s", pmc.configFileName)
		return
	}
	glog.Info(parentDS.String())
	pmc.parentDS = &parentDS

	select {
	case pmc.eventCh <- event.Event{
		Source:    event.PMC,
		CfgName:   pmc.configFileName,
		ClockType: event.ClockType(pmc.clockType),
		Time:      time.Now().UnixMilli(),
		Data:      &event.ParentDSData{ParentDataSet: parentDS},
	}:
	default:
		glog.Warning("event channel full, dropping ParentDS update")
	}
}

// Monitor continuously monitors the PMC process and handles restarts.
func (pmc *PMCProcess) Monitor() error {
	for {
		err := pmc.monitor()
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
