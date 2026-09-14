package daemon

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ublox"
	gpsdlib "github.com/stratoberry/go-gpsd"
)

const (
	GPSD_PROCESSNAME     = "gpsd"
	GNSSMONITOR_INTERVAL = 1 * time.Second
)

type filteringStderrWriter struct{}

// Write filters a known harmless gpsd ioctl warning before forwarding output.
func (w *filteringStderrWriter) Write(p []byte) (n int, err error) {
	if bytes.Contains(p, []byte("Inappropriate ioctl for device")) {
		// Suppress this error
		return len(p), nil
	}
	// Write all other output to the real stderr (container logs)
	return os.Stderr.Write(p)
}

type GPSD struct {
	name                 string
	execMutex            sync.Mutex
	cmdLine              string
	cmd                  *exec.Cmd
	serialPort           string
	exitCh               chan struct{}
	stopped              bool
	noFixStateOccurrence int // number of times no fix state has occurred
	offset               int64
	gpsStatus            int64
	gpsStatusValid       bool
	processConfig        config.ProcessConfig
	gmInterface          string
	messageTag           string
	ublxTool             *ublox.UBlox
	gnssInitCmds         ublox.CommandList      // optional HardwareConfig GNSS init commands
	gnssResultsFn        func(results []string) // callback to store GNSS init results
	gpsdSession          *gpsdlib.Session
	gpsdDoneCh           chan bool
	sourceLost           bool
	monitorCtx           context.Context
	monitorCancel        context.CancelFunc
	// cmdRunner executes an external command; defaults to exec.CommandContext and
	// can be overridden in tests to inject a fake command.
	cmdRunner func(ctx context.Context, name string, args ...string) *exec.Cmd
}

// MonitorProcess ... Monitor GPSD process
func (g *GPSD) MonitorProcess(p config.ProcessConfig) {
	g.processConfig = p
}

// Name ... Process name
func (g *GPSD) Name() string {
	return g.name
}

// ExitCh ... exit channel
func (g *GPSD) ExitCh() chan struct{} {
	return g.exitCh
}

// SerialPort ... get SerialPort
func (g *GPSD) SerialPort() string {
	return g.serialPort
}

// setStopped records whether the GPSD process has stopped.
func (g *GPSD) setStopped(val bool) {
	g.execMutex.Lock()
	g.stopped = val
	g.execMutex.Unlock()
}

// Stopped ...
func (g *GPSD) Stopped() bool {
	g.execMutex.Lock()
	me := g.stopped
	g.execMutex.Unlock()
	return me
}

// CmdStop .... stop
func (g *GPSD) CmdStop() {
	glog.Infof("stopping %s...", g.name)
	// Need to ensure cleanup to workaround ublox cpu spike bug
	if g.ublxTool != nil {
		g.ublxTool.UbloxPollStop()
	}
	if g.cmd == nil {
		return
	}
	g.setStopped(true)
	g.ProcessStatus(PtpProcessDown)
	if g.cmd.Process != nil {
		glog.Infof("Sending TERM to PID: %d", g.cmd.Process.Pid)
		err := g.cmd.Process.Signal(syscall.SIGTERM)
		if err != nil {
			glog.Infof("Process %s (%d) failed to terminate", g.name, g.cmd.Process.Pid)
		}
	}
	<-g.exitCh // waiting for all child routines to exit; we could add timeout to avoid waiting
	g.monitorCancel()
	glog.Infof("Process %s terminated", g.name)
}

// CmdInit ... initialize GPSD
func (g *GPSD) CmdInit() {
	if g.name == "" {
		g.name = GPSD_PROCESSNAME
	}
	g.monitorCtx, g.monitorCancel = context.WithCancel(context.Background())
	g.cmdLine = fmt.Sprintf("/usr/local/sbin/%s -p -n -S 2947 -G -N %s", g.Name(), g.SerialPort())
	if g.cmdRunner == nil {
		g.cmdRunner = exec.CommandContext
	}
}

// resetSerialPort resets the serial device to a sane state before starting gpsd.
// On platforms where GNSS is connected via UART (e.g. HPE), an abrupt gpsd
// termination can leave the UART in a dirty state that prevents a new gpsd
// instance from communicating with the GNSS module. Running "stty sane" clears
// that state. The reset is unconditional — it is also harmless on platforms
// with USB-connected GNSS devices.
// If serialPort is empty the reset is skipped with a warning.
// A reset failure is logged as a warning but never blocks gpsd startup.
func (g *GPSD) resetSerialPort(ctx context.Context) error {
	if g.serialPort == "" {
		glog.Warningf("gpsd: no serial port configured, skipping device reset before gpsd start")
		return nil
	}
	out, err := g.cmdRunner(ctx, "stty", "-F", g.serialPort, "sane").CombinedOutput()
	if err != nil {
		return fmt.Errorf("stty -F %s sane: %w (output: %s)", g.serialPort, err, strings.TrimSpace(string(out)))
	}
	glog.Infof("gpsd: serial port %s reset to sane state", g.serialPort)
	return nil
}

// ProcessStatus ...
func (g *GPSD) ProcessStatus(status int64) {
	processStatus(g.name, g.messageTag, status)
}

// CmdRun ... run GPSD
func (g *GPSD) CmdRun() {
	go g.MonitorGNSSEventsWithUblox()

	for {
		g.ProcessStatus(PtpProcessUp)
		glog.Infof("Starting %s...", g.Name())
		glog.Infof("%s cmd: %+v", g.Name(), g.cmd)
		g.cmd.Stderr = &filteringStderrWriter{}
		var err error
		// Don't restart after termination
		if !g.Stopped() {
			time.Sleep(1 * time.Second)
			if resetErr := g.resetSerialPort(g.monitorCtx); resetErr != nil {
				glog.Warningf("gpsd: proceeding with start despite serial port reset failure: %v", resetErr)
			}
			err = g.cmd.Start() // this is asynchronous call,
			if err != nil {
				glog.Errorf("CmdRun() error starting %s: %v", g.Name(), err)
			}
			err = g.cmd.Wait()
			if err != nil {
				glog.Errorf("CmdRun() error waiting for %s: %v", g.Name(), err)
			}
		}
		time.Sleep(connectionRetryInterval) // Delay to prevent flooding restarts if startup fails
		// Don't restart after termination
		if g.Stopped() {
			glog.Infof("not recreating %s...", g.name)
			g.exitCh <- struct{}{} // cmdStop is waiting for confirmation
			break
		} else {
			glog.Infof("Recreating %s...", g.name)
			newCmd := exec.Command(g.cmd.Args[0], g.cmd.Args[1:]...)
			g.cmd = newCmd
		}
	}
}

// MonitorGNSSEventsWithUblox ... monitor GNSS events with ublox
func (g *GPSD) MonitorGNSSEventsWithUblox() {
	ticker := time.NewTicker(GNSSMONITOR_INTERVAL)
	// ensure we cancel the ticker and send our termination event on return
	defer func() {
		select {
		case g.processConfig.EventChannel <- event.Event{
			Source:    event.GNSS,
			CfgName:   g.processConfig.ConfigName,
			ClockType: g.processConfig.ClockType,
			Time:      time.Now().UnixMilli(),
			Reset:     true,
		}:
		default:
			glog.Error("failed to send gnss terminated event to eventHandler")
		}
		ticker.Stop()
	}()
	for {
		ublx, err := ublox.NewUblox(g.gnssInitCmds...)
		if err != nil {
			glog.Errorf("failed to initialize GNSS monitoring via ublox %s", err)
			select {
			case <-g.monitorCtx.Done():
				return
			case <-time.After(GNSSMONITOR_INTERVAL):
				continue
			}
		}
		g.ublxTool = ublx
		if results := ublx.InitResults(); len(results) > 0 && g.gnssResultsFn != nil {
			g.gnssResultsFn(results)
		}
		g.gpsStatus = 0
		g.gpsStatusValid = false
		subscription := ublx.Subscribe(g.monitorCtx,
			ublox.NavClockType,
			ublox.NavStatusType,
			ublox.NavTimeLsType,
		)
		missedTickers := 0
		sinceLastTick := false
		ublx.UbloxPollInit()
		for {
			select {
			case <-ticker.C:
				if sinceLastTick {
					missedTickers = 0
				} else {
					missedTickers++
					if missedTickers > 3 {
						g.gpsStatusValid = false
						ublx.UbloxPollReset()
						missedTickers = 0
					}
				}
				sinceLastTick = false
				ublx.UbloxPollInit()
			case message, ok := <-subscription.Messages:
				if !ok {
					return
				}
				if g.processGNSSMessage(message) {
					sinceLastTick = true
				}
			case <-g.monitorCtx.Done():
				return
			}
		}
	}
}

// processGNSSMessage applies one typed ublox message. NAV-CLOCK completes a
// GNSS sample using the most recent NAV-STATUS message.
func (g *GPSD) processGNSSMessage(message ublox.Message) bool {
	switch payload := message.Payload.(type) {
	case ublox.NavStatus:
		g.gpsStatus = payload.GPSFix
		g.gpsStatusValid = true
	case ublox.NavClock:
		if !g.gpsStatusValid {
			return false
		}
		g.processGNSSResult(ublox.PollResult{
			GPSStatus: g.gpsStatus,
			Offset:    payload.Offset,
			HasGNSS:   true,
		})
		return true
	case ublox.TimeLs:
		g.processGNSSResult(ublox.PollResult{TimeLs: &payload})
	}
	return false
}

// processGNSSResult applies parsed ublox data to daemon state and emits the
// corresponding GNSS and leap-second events.
func (g *GPSD) processGNSSResult(result ublox.PollResult) {
	if result.HasGNSS {
		g.offset = result.Offset
		g.sourceLost = result.GPSStatus < 3 || !g.isOffsetInRange()
		if g.processConfig.EventChannel != nil {
			select {
			case g.processConfig.EventChannel <- event.Event{
				Source:     event.GNSS,
				CfgName:    g.processConfig.ConfigName,
				IFace:      g.gmInterface,
				ClockType:  g.processConfig.ClockType,
				Time:       time.Now().UnixMilli(),
				WriteToLog: true,
				Reset:      false,
				Data:       &event.GNSSData{GPSStatus: result.GPSStatus, Offset: g.offset, SourceLost: g.sourceLost},
			}:
			default:
				glog.Error("failed to send gnss event to eventHandler")
			}
		}
	}
	if result.TimeLs != nil && leap.LeapMgr != nil {
		select {
		case leap.LeapMgr.UbloxLsInd <- *result.TimeLs:
		case <-time.After(100 * time.Millisecond):
			glog.Infof("failed to send leap event updates")
		}
	}
}

// isOffsetInRange returns true when abs(offset) < GMThreshold.Max
// (non-inclusive boundary). GMThreshold.Min is deprecated and intentionally
// ignored here.
func (g *GPSD) isOffsetInRange() bool {
	return math.Abs(float64(g.offset)) < float64(g.processConfig.GMThreshold.Max)
}
