package daemon

import (
	"fmt"
	"net"
	"os"
	"strings"
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
	pmcCmdPrefix   = `pmc -u -b 0 -f /var/run/%s "%s"`
	PMCProcessName = "pmc"
)

func NewPMCProcess(runID int, eventHandler *event.EventHandler) *PMCProcess {
	return &PMCProcess{
		configFileName:    fmt.Sprintf("ptp4l.%d.config", runID),
		messageTag:        fmt.Sprintf("[ptp4l.%d.config:{level}]", runID),
		monitorParentData: true,
	}
}

type PMCProcess struct {
	configFileName        string
	stopped               bool
	cmd                   *expect.GExpect
	monitorPortState      bool
	monitorTimeSync       bool
	monitorParentData     bool
	monitorCMLDS          bool
	GrandmasterClockClass uint8
	exitCh                chan struct{}
	profileClockType      string
	c                     net.Conn
	messageTag            string
	cmdLine               string
	eventHandler          *event.EventHandler
}

func (pmc *PMCProcess) Name() string {
	return PMCProcessName
}

func (pmc *PMCProcess) Stopped() bool {
	return pmc.stopped
}

func (pmc *PMCProcess) CmdStop() {
	pmc.stopped = true
	pmc.exitCh <- struct{}{}
}

func (pmc *PMCProcess) CmdInit() {
	pmc.stopped = false
}

func (pmc *PMCProcess) ProcessStatus(c net.Conn, status int64) {
	if c != nil {
		pmc.c = c
	}
	processStatus(pmc.c, PMCProcessName, pmc.messageTag, status)
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

const (
	pollTimeout = 3 * time.Second
)

func (pmc *PMCProcess) EmitClockClassLogs(c net.Conn) {
	if c != nil {
		pmc.c = c
	}
	utils.EmitClockClass(c, ptp4lProcessName, pmc.configFileName, pmc.GrandmasterClockClass)
}

func (pmc *PMCProcess) PollClockClass() error {
	parentDS, err := pmcPkg.RunPMCExpGetParentDS(pmc.configFileName)
	if err != nil {
		return err
	}
	pmc.GrandmasterClockClass = parentDS.GrandmasterClockClass
	return nil
}

func (pmc *PMCProcess) CmdRun(stdToSocket bool) {
	err := pmc.PollClockClass()
	if err != nil {
		glog.Error("Failed to initalise clock class")
	}
	go func() {
		for {
			var c net.Conn
			if stdToSocket {
				cAttempt, err := dialSocket()
				for err != nil {
					cAttempt, err = dialSocket()
				}
				c = cAttempt
			}
			err := pmc.Monitor(c)
			if err == nil {
				// No error completed gracefully
				return
			}
		}
	}()
}

func (pmc *PMCProcess) monitor(c net.Conn) error {
	exp, r, err := pmcPkg.GetPMCMontior(pmc.configFileName)
	if err != nil {
		return err
	}

	subscribeCmd := pmc.getMonitorSubcribeCommand()
	glog.Infof("Sending '%s' to pmc", subscribeCmd)
	exp.Send(subscribeCmd + "\n")
	for {
		select {
		case <-r:
			glog.Warningf("PMC monitoring process exited")
			return fmt.Errorf("PMC needs to restart")
		case <-pmc.exitCh:
			err := exp.SendSignal(os.Kill)
			glog.Warningf("pmc failed to send signal to pmc process %s", err)
			return nil // TODO close gracefully
		default:
			_, matches, err := exp.Expect(pmcPkg.GetMonitorRegex(pmc.monitorParentData), pollTimeout)
			if err != nil {
				if strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "exit") {
					glog.Warningf("PMC process exited (%v)", err)
					return fmt.Errorf("PMC needs to restart")
				}
				glog.Errorf("Error waiting for notification: %v", err)
				continue
			}
			if len(matches) == 0 {
				continue
			}
			if strings.Contains(matches[0], "PARENT_DATA_SET") {
				processedMessage, err := protocol.ProcessMessage[*protocol.ParentDataSet](matches)
				if err != nil {
					glog.Warningf("failed to process message for PARENT_DATA_SET: %s", err)
				}
				if pmc.GrandmasterClockClass != processedMessage.GrandmasterClockClass {
					pmc.GrandmasterClockClass = processedMessage.GrandmasterClockClass
					pmc.eventHandler.AnnounceClockClass(int(pmc.GrandmasterClockClass), pmc.configFileName, pmc.c)
				}
				if pmc.profileClockType == TBC {
					pmc.eventHandler.DownstreamAnnounceIWF(pmc.configFileName, pmc.c)
				}
			}
		}
	}
}

func (pmc *PMCProcess) Monitor(c net.Conn) error {
	for {
		err := pmc.monitor(c)
		if err != nil {
			// If there is an error we need to restart
			glog.Info("pmc process hit an issue, restarting...")
			continue
		}
		return err
	}
}

func (pmc *PMCProcess) ExitCh() chan struct{} {
	return pmc.exitCh
}

func (pmc *PMCProcess) MonitorProcess(processCfg config.ProcessConfig) {
}
