package event_test

import (
	"bufio"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/testhelpers"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fbprotocol "github.com/facebook/time/ptp/protocol"
)

func TestMain(m *testing.M) {
	teardownTests := testhelpers.SetupTests()
	defer teardownTests()
	os.Exit(m.Run())
}

var (
	staleSocketTimeout = 100 * time.Millisecond
)

func newTestPMCMock() *pmc.MockClient {
	return &pmc.MockClient{
		GMSettingsResult: protocol.GrandmasterSettings{
			ClockQuality: fbprotocol.ClockQuality{
				ClockClass:              0,
				ClockAccuracy:           0,
				OffsetScaledLogVariance: 0,
			},
			TimePropertiesDS: protocol.TimePropertiesDS{
				CurrentUtcOffset:      0,
				CurrentUtcOffsetValid: false,
				Leap59:                false,
				Leap61:                false,
				TimeTraceable:         true,
				FrequencyTraceable:    true,
				PtpTimescale:          false,
				TimeSource:            0,
			},
		},
	}
}

type PTPEvents struct {
	processName      event.EventSource
	clockState       event.PTPState
	cfgName          string
	iface            string
	outOfSpec        bool
	values           map[event.ValueType]interface{}
	wantGMState      string // want is the expected output.
	wantClockState   string
	wantProcessState string
	desc             string
	sourceLost       bool
}

func TestEventHandler_ProcessEvents(t *testing.T) {
	pmcMock := newTestPMCMock()
	pmc.SetMock(pmcMock)
	defer pmc.ResetMock()
	tests := []PTPEvents{
		{
			processName:      event.DPLL,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_LOCKED,
			outOfSpec:        false,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0), event.PHASE_STATUS: 3, event.FREQUENCY_STATUS: 3, event.PPS_STATUS: 1},
			wantGMState:      "GM[0]:[ts2phc.0.config] unknown T-GM-STATUS s0",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 248",
			wantProcessState: "dpll[0]:[ts2phc.0.config] ens1f0 frequency_status 3 offset 0 phase_status 3 pps_status 1 s2",
			desc:             "Initial state, gnss is not set, so expect GM to be FREERUN",
		},
		{
			processName:      event.GNSS,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_LOCKED,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0), event.GPS_STATUS: 3},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s0",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 248",
			wantProcessState: "gnss[0]:[ts2phc.0.config] ens1f0 gnss_status 3 offset 0 s2",
			desc:             "gnss is locked and has dpll in locked state,but ts2phc has not yet reported so GM will be FREERUN ",
		},
		{
			processName:      event.TS2PHCProcessName,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_LOCKED,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0)},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s2",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 6",
			wantProcessState: "ts2phc[0]:[ts2phc.0.config] ens1f0 offset 0 s2",
			desc:             "ts2phc is now reported as locked, GM should be in locked state",
		},
		{
			processName:      event.TS2PHCProcessName,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_FREERUN,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(5000)},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s0",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 248",
			wantProcessState: "ts2phc[0]:[ts2phc.0.config] ens1f0 offset 5000 s0",
			desc:             "ts2phc is reporting FREERUN, GM should be in FREERUN state",
		},
		{
			processName:      event.TS2PHCProcessName,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_LOCKED,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0)},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s2",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 6",
			wantProcessState: "ts2phc[0]:[ts2phc.0.config] ens1f0 offset 0 s2",
			desc:             "ts2phc is also reporting locked, GM should be in locked state",
		},
		{
			processName:      event.GNSS,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_FREERUN,
			outOfSpec:        false,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0), event.GPS_STATUS: 0},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s2",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 6",
			wantProcessState: "gnss[0]:[ts2phc.0.config] ens1f0 gnss_status 0 offset 0 s0",
			sourceLost:       true,
			desc:             "GPS is free run ,source is lost when everything else is locked(Do nothing and wait  for DPLL to switch to HOLDOVER)",
		},
		{
			processName:      event.DPLL,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_HOLDOVER,
			outOfSpec:        false,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0), event.PHASE_STATUS: 4, event.FREQUENCY_STATUS: 4, event.PPS_STATUS: 1},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s1",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 7",
			wantProcessState: "dpll[0]:[ts2phc.0.config] ens1f0 frequency_status 4 offset 0 phase_status 4 pps_status 1 s1",
			desc:             "dpll is on Holdover, where source is lost, move to holdover state",
		},
		{
			processName:      event.DPLL,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_FREERUN,
			outOfSpec:        true,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0), event.PHASE_STATUS: 1, event.FREQUENCY_STATUS: 1, event.PPS_STATUS: 1},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s0",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 248",
			wantProcessState: "dpll[0]:[ts2phc.0.config] ens1f0 frequency_status 1 offset 0 phase_status 1 pps_status 1 s0",
			desc:             "dpll move to FREERUN from holdover (out of spec)",
		},
		{
			processName:      event.GNSS,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_LOCKED,
			outOfSpec:        false,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0), event.GPS_STATUS: 3},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s0",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 140",
			wantProcessState: "gnss[0]:[ts2phc.0.config] ens1f0 gnss_status 3 offset 0 s2",
			sourceLost:       false,
			desc:             "GPS is locked but dpll is in FREERUN and out of spec, yet to switch over in that case GM should stay with last state",
		},
		{
			processName:      event.DPLL,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_LOCKED,
			outOfSpec:        true,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0), event.PHASE_STATUS: 3, event.FREQUENCY_STATUS: 3, event.PPS_STATUS: 1},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s2",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 6",
			wantProcessState: "dpll[0]:[ts2phc.0.config] ens1f0 frequency_status 3 offset 0 phase_status 3 pps_status 1 s2",
			desc:             "everything is in locked state",
		},
		{
			processName:      event.TS2PHCProcessName,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_HOLDOVER,
			outOfSpec:        true,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(99999), event.NMEA_STATUS: 0},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s1",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 7",
			wantProcessState: "ts2phc[0]:[ts2phc.0.config] ens1f0 nmea_status 0 offset 99999 s1",
			desc:             "ts2phc is not in locked state",
		},
		{
			processName:      event.TS2PHCProcessName,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_FREERUN,
			outOfSpec:        true,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(99999), event.NMEA_STATUS: 0},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s0",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 248",
			wantProcessState: "ts2phc[0]:[ts2phc.0.config] ens1f0 nmea_status 0 offset 99999 s0",
			desc:             "ts2phc is not in locked state",
		},
		{
			processName:      event.TS2PHCProcessName,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_LOCKED,
			outOfSpec:        true,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0), event.NMEA_STATUS: 1},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s2",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 6",
			wantProcessState: "ts2phc[0]:[ts2phc.0.config] ens1f0 nmea_status 1 offset 0 s2",
			desc:             "everything is in locked state",
		},
		{
			processName:      event.TS2PHCProcessName,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_FREERUN,
			outOfSpec:        true,
			iface:            "ens2f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(5000), event.PPS_STATUS: 1},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s0",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 248",
			wantProcessState: "ts2phc[0]:[ts2phc.0.config] ens2f0 offset 5000 pps_status 1 s0",
			desc:             "2nd card ts2phc offset spiked",
		},
		{
			processName:      event.TS2PHCProcessName,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_LOCKED,
			outOfSpec:        true,
			iface:            "ens2f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0), event.NMEA_STATUS: 1},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s2",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 6",
			wantProcessState: "ts2phc[0]:[ts2phc.0.config] ens2f0 nmea_status 1 offset 0 s2",
			desc:             "2nd card restored ",
		},
		{ // add scenario where first GNSS is lost and then DPLL 1 and 2 both  is switching to HOLDOVER
			processName:      event.GNSS,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_FREERUN,
			outOfSpec:        false,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0), event.GPS_STATUS: 0},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s2",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 6",
			wantProcessState: "gnss[0]:[ts2phc.0.config] ens1f0 gnss_status 0 offset 0 s0",
			sourceLost:       true,
			desc:             "Case 2: GPS is free run ,source is lost when everything else is locked(Do nothing and wait  for DPLL to switch to HOLDOVER)",
		},
		{
			processName:      event.DPLL,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_HOLDOVER,
			outOfSpec:        false,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0), event.PHASE_STATUS: 4, event.FREQUENCY_STATUS: 4, event.PPS_STATUS: 1},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s1",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 7",
			wantProcessState: "dpll[0]:[ts2phc.0.config] ens1f0 frequency_status 4 offset 0 phase_status 4 pps_status 1 s1",
			desc:             "dpll is on Holdover, where source is lost, moving to holdover state",
		},
		{
			processName:      event.DPLL,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_HOLDOVER,
			outOfSpec:        false,
			iface:            "ens2f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0), event.PHASE_STATUS: 4, event.FREQUENCY_STATUS: 4, event.PPS_STATUS: 0},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s1",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 7",
			wantProcessState: "dpll[0]:[ts2phc.0.config] ens2f0 frequency_status 4 offset 0 phase_status 4 pps_status 0 s1",
			desc:             "dpll 2 is on Holdover, where source is lost, moving to holdover state",
		},
		{ // 2nd card spiking stay in holdover
			processName:      event.TS2PHCProcessName,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_FREERUN,
			outOfSpec:        false,
			iface:            "ens2f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(5000), event.PPS_STATUS: 0},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s1",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 7",
			wantProcessState: "ts2phc[0]:[ts2phc.0.config] ens2f0 offset 5000 pps_status 0 s0",
			desc:             "2nd card ts2phc offset spiked when in holdover",
		},
	}

	logOut := make(chan string, 100)
	eChannel := make(chan event.Event, 100)
	closeChn := make(chan bool)
	go listenToEvents(closeChn, logOut)
	eventManager := event.Init("node", true, "/tmp/go.sock", eChannel, closeChn, nil, nil, nil, nil)
	go eventManager.ProcessEvents()
	assert.NoError(t, leap.MockLeapFile())
	defer func() {
		close(leap.LeapMgr.Close)
		// Sleep to allow context to switch
		time.Sleep(100 * time.Millisecond)
		assert.Nil(t, leap.LeapMgr)
	}()
	time.Sleep(1 * time.Second)
	for _, test := range tests {
		select {
		case eChannel <- sendEvents(test.cfgName, test.iface, test.processName, test.clockState, test.values, test.outOfSpec, test.sourceLost):
			log.Println("sent data to channel")
			log.Println(test.cfgName, test.processName, test.clockState, test.outOfSpec, test.values)
			time.Sleep(1 * time.Second)
		default:
			log.Println("nothing to read")
		}
	retry:
		for i := 0; i < len(logOut); i++ {
			select {
			case c := <-logOut:
				s1 := strings.Index(c, "[")
				s2 := strings.Index(c, "]")
				rs := strings.Replace(c, c[s1+1:s2], "0", -1)

				if strings.HasPrefix(c, string(test.processName)) {
					assert.Equal(t, test.wantProcessState, rs, test.desc)
				}
				if strings.HasPrefix(c, "GM[") {
					assert.Equal(t, test.wantGMState, rs, test.desc)
				}
				if strings.HasPrefix(c, "ptp4l[") {
					assert.Equal(t, test.wantClockState, rs, test.desc)
				}
			default:
			}
			if len(logOut) > 0 {
				goto retry
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	closeChn <- true
	time.Sleep(1 * time.Second)
}

func listenToEvents(closeChn chan bool, logOut chan string) {
	l, sErr := Listen("/tmp/go.sock")
	if sErr != nil {
		glog.Infof("error setting up socket %s", sErr)
		return
	}
	glog.Infof("connection established successfully")

	for {
		select {
		case <-closeChn:
			log.Println("closing socket")
			return
		default:
			fd, err := l.Accept()
			if err != nil {
				glog.Infof("accept error: %s", err)
			} else {
				go ProcessTestEvents(fd, logOut)
			}
		}
	}
}

// Listen ... listen to ptp daemon logs
func Listen(addr string) (l net.Listener, e error) {
	uAddr, err := net.ResolveUnixAddr("unix", addr)
	if err != nil {
		return nil, err
	}

	// Try to listen on the socket. If that fails we check to see if it's a stale
	// socket and remove it if it is. Then we try to listen one more time.
	l, err = net.ListenUnix("unix", uAddr)
	if err != nil {
		if err = removeIfStaleUnixSocket(addr); err != nil {
			return nil, err
		}
		if l, err = net.ListenUnix("unix", uAddr); err != nil {
			return nil, err
		}
	}
	return l, err
}

// removeIfStaleUnixSocket takes in a path and removes it iff it is a socket
// that is refusing connections
func removeIfStaleUnixSocket(socketPath string) error {
	// Ensure it's a socket; if not return without an error
	if st, err := os.Stat(socketPath); err != nil || st.Mode()&os.ModeType != os.ModeSocket {
		return nil
	}
	// Try to connect
	conn, err := net.DialTimeout("unix", socketPath, staleSocketTimeout)
	if err != nil { // =syscall.ECONNREFUSED {
		return os.Remove(socketPath)
	}
	return conn.Close()
}

func ProcessTestEvents(c net.Conn, logOut chan<- string) {
	// echo received messages
	remoteAddr := c.RemoteAddr().String()
	log.Println("Client connected from", remoteAddr)
	scanner := bufio.NewScanner(c)
	for {
		ok := scanner.Scan()
		if !ok {
			break
		}
		msg := scanner.Text()
		glog.Infof("events received %s", msg)
		logOut <- msg
	}
}

func sendEvents(cfgName string, iface string, processName event.EventSource, state event.PTPState,
	values map[event.ValueType]interface{}, outOfSpec bool, sourceLost bool) event.Event {
	glog.Info("sending Nav status event to event handler Process")
	e := event.Event{
		Source:     processName,
		IFace:      iface,
		CfgName:    cfgName,
		ClockType:  "GM",
		Time:       0,
		WriteToLog: true,
		Reset:      false,
	}
	if processName == event.GNSS {
		var gpsStatus int64
		if gps, ok := values[event.GPS_STATUS]; ok {
			gpsStatus = int64(gps.(int))
		}
		var offset int64
		if off, ok := values[event.OFFSET]; ok {
			offset = off.(int64)
		}
		e.Data = &event.GNSSData{GPSStatus: gpsStatus, Offset: offset, SourceLost: sourceLost}
	} else {
		e.Data = &event.PTPData{
			State:      state,
			Values:     values,
			OutOfSpec:  outOfSpec,
			SourceLost: sourceLost,
		}
	}
	return e
}

func sendBCEvent(cfgName string, processName event.EventSource,
	state event.PTPState, values map[event.ValueType]interface{},
	sourceLost bool) event.Event {
	return event.Event{
		Source:     processName,
		IFace:      "ens1f0",
		CfgName:    cfgName,
		ClockType:  "BC",
		Time:       time.Now().UnixMilli(),
		WriteToLog: true,
		Data: &event.PTPData{
			State:      state,
			Values:     values,
			SourceLost: sourceLost,
		},
	}
}

func drainForPattern(logOut <-chan string, pattern string, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		select {
		case msg := <-logOut:
			if strings.Contains(msg, pattern) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func TestBCClockClassThroughProcessEvents(t *testing.T) {
	pmcMock := &pmc.MockClient{
		ParentTimeCurrentDSResult: pmc.ParentTimeCurrentDS{
			ParentDataSet: protocol.ParentDataSet{
				GrandmasterClockClass:    6,
				GrandmasterClockAccuracy: 0x21,
			},
			TimePropertiesDS: protocol.TimePropertiesDS{
				CurrentUtcOffset:      37,
				CurrentUtcOffsetValid: true,
				PtpTimescale:          true,
				TimeTraceable:         true,
			},
			CurrentDS: protocol.CurrentDS{StepsRemoved: 1},
		},
	}
	pmc.SetMock(pmcMock)
	defer pmc.ResetMock()

	dir, err := os.MkdirTemp("", "bc-test")
	assert.NoError(t, err)
	defer os.RemoveAll(dir)
	socketPath := filepath.Join(dir, "t.sock")

	logOut := make(chan string, 100)
	listener, err := Listen(socketPath)
	assert.NoError(t, err)
	defer listener.Close()
	go func() {
		for {
			fd, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go ProcessTestEvents(fd, logOut)
		}
	}()

	eChannel := make(chan event.Event, 100)
	closeChn := make(chan bool)
	assert.NoError(t, leap.MockLeapFile())
	defer func() {
		close(leap.LeapMgr.Close)
		time.Sleep(100 * time.Millisecond)
	}()

	eventManager := event.Init("node", true, socketPath, eChannel, closeChn, nil, nil, nil, nil)
	go eventManager.ProcessEvents()
	defer func() { closeChn <- true }()

	clk, addErr := eventManager.AddClock("ptp4l.0.config", event.BC)
	assert.NoError(t, addErr)
	bc := clk.(*event.BCClock)
	bc.UpdateUpstreamParentDataSet(protocol.ParentDataSet{
		GrandmasterClockClass:    6,
		GrandmasterClockAccuracy: 0x21,
	})

	time.Sleep(500 * time.Millisecond)

	const (
		bcCfgDPLL  = "ts2phc.0.config"
		bcCfgPTP4l = "ptp4l.0.config"
	)

	// First DPLL event carries leading source configuration.
	// AddEvent's first call creates the detail but does NOT insert into the
	// window, so we need WindowSize+1 events per source to fill the window.
	eChannel <- sendBCEvent(bcCfgDPLL, event.DPLL, event.PTP_LOCKED,
		map[event.ValueType]interface{}{
			event.LeadingSource:            true,
			event.InSyncConditionThreshold: uint64(10000),
			event.InSyncConditionTimes:     uint64(1),
			event.ToFreeRunThreshold:       uint64(1500),
			event.MaxInSpecOffset:          uint64(500),
			event.OFFSET:                   int64(10),
		}, false)
	time.Sleep(100 * time.Millisecond)

	// 10 more DPLL events to fill the DPLL window (first event created the detail,
	// these 10 fill WindowSize=10)
	for i := 0; i < 10; i++ {
		eChannel <- sendBCEvent(bcCfgDPLL, event.DPLL, event.PTP_LOCKED,
			map[event.ValueType]interface{}{event.OFFSET: int64(10)}, false)
		time.Sleep(50 * time.Millisecond)
	}

	// First PTP4l event carries downstream port configuration
	eChannel <- sendBCEvent(bcCfgPTP4l, event.PTP4l, event.PTP_LOCKED,
		map[event.ValueType]interface{}{
			event.ControlledPortsConfig: "ptp4l.0.config",
			event.ClockIDKey:            "001122.fffe.334455",
			event.OFFSET:                int64(10),
		}, false)
	time.Sleep(100 * time.Millisecond)

	// 10 more PTP4l events to fill the PTP4l window; the last triggers FREERUN→LOCKED
	for i := 0; i < 10; i++ {
		eChannel <- sendBCEvent(bcCfgPTP4l, event.PTP4l, event.PTP_LOCKED,
			map[event.ValueType]interface{}{event.OFFSET: int64(10)}, false)
		time.Sleep(50 * time.Millisecond)
	}

	// One more DPLL event in LOCKED state triggers "stay LOCKED" path:
	// upstream ParentDataSet (class 6) != downstream (class 0) → needsDownstreamUpdate
	// → downstreamAnnounceIWF → announceClockClass(6)
	eChannel <- sendBCEvent(bcCfgDPLL, event.DPLL, event.PTP_LOCKED,
		map[event.ValueType]interface{}{event.OFFSET: int64(10)}, false)

	assert.True(t, drainForPattern(logOut, "CLOCK_CLASS_CHANGE 6", 5*time.Second),
		"expected CLOCK_CLASS_CHANGE 6 after BC reaches LOCKED")

	// Phase 2: PTP4l source lost triggers LOCKED→HOLDOVER (class 135)
	// → announceLocalData → announceClockClass(135)
	eChannel <- sendBCEvent(bcCfgPTP4l, event.PTP4l, event.PTP_FREERUN,
		map[event.ValueType]interface{}{event.OFFSET: int64(10)}, true)

	assert.True(t, drainForPattern(logOut, "CLOCK_CLASS_CHANGE 135", 5*time.Second),
		"expected CLOCK_CLASS_CHANGE 135 after BC enters HOLDOVER")
}

func waitForMetric(gauge *prometheus.GaugeVec, cfgName string, expected float64, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		val := testutil.ToFloat64(gauge.With(prometheus.Labels{
			"process": "ptp4l", "config": cfgName, "node": "testnode", //nolint:goconst
		}))
		if val == expected {
			return true
		}
		select {
		case <-deadline:
			return false
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestBCClockClassMetric(t *testing.T) {
	pmcMock := &pmc.MockClient{
		ParentTimeCurrentDSResult: pmc.ParentTimeCurrentDS{
			ParentDataSet: protocol.ParentDataSet{
				GrandmasterClockClass:    6,
				GrandmasterClockAccuracy: 0x21,
			},
			TimePropertiesDS: protocol.TimePropertiesDS{
				CurrentUtcOffset:      37,
				CurrentUtcOffsetValid: true,
				PtpTimescale:          true,
				TimeTraceable:         true,
			},
			CurrentDS: protocol.CurrentDS{StepsRemoved: 1},
		},
	}
	pmc.SetMock(pmcMock)
	defer pmc.ResetMock()

	assert.NoError(t, leap.MockLeapFile())
	defer func() {
		close(leap.LeapMgr.Close)
		time.Sleep(100 * time.Millisecond)
	}()

	clockClassGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "test_ptp_clock_class",
			Help: "test clock class metric",
		},
		[]string{"process", "config", "node"},
	)
	offsetGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "test_ptp_offset",
			Help: "test offset metric",
		},
		[]string{"from", "process", "node", "iface"},
	)
	clockStateGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "test_ptp_clock_state",
			Help: "test clock state metric",
		},
		[]string{"process", "node", "iface"},
	)

	eChannel := make(chan event.Event, 100)
	closeChn := make(chan bool)
	eventManager := event.Init("testnode", false, "", eChannel, closeChn, offsetGauge, clockStateGauge, clockClassGauge, nil)
	go eventManager.ProcessEvents()
	defer func() { closeChn <- true }()

	clk, addErr := eventManager.AddClock("ptp4l.0.config", event.BC)
	assert.NoError(t, addErr)
	bc := clk.(*event.BCClock)
	bc.UpdateUpstreamParentDataSet(protocol.ParentDataSet{
		GrandmasterClockClass:    6,
		GrandmasterClockAccuracy: 0x21,
	})

	time.Sleep(500 * time.Millisecond)

	const (
		bcCfgDPLL  = "ts2phc.0.config"
		bcCfgPTP4l = "ptp4l.0.config"
	)

	// Fill DPLL window: first event creates detail, next 10 fill WindowSize=10
	eChannel <- sendBCEvent(bcCfgDPLL, event.DPLL, event.PTP_LOCKED,
		map[event.ValueType]interface{}{
			event.LeadingSource:            true,
			event.InSyncConditionThreshold: uint64(10000),
			event.InSyncConditionTimes:     uint64(1),
			event.ToFreeRunThreshold:       uint64(1500),
			event.MaxInSpecOffset:          uint64(500),
			event.OFFSET:                   int64(10),
		}, false)
	time.Sleep(100 * time.Millisecond)

	for i := 0; i < 10; i++ {
		eChannel <- sendBCEvent(bcCfgDPLL, event.DPLL, event.PTP_LOCKED,
			map[event.ValueType]interface{}{event.OFFSET: int64(10)}, false)
		time.Sleep(50 * time.Millisecond)
	}

	// Fill PTP4l window
	eChannel <- sendBCEvent(bcCfgPTP4l, event.PTP4l, event.PTP_LOCKED,
		map[event.ValueType]interface{}{
			event.ControlledPortsConfig: "ptp4l.0.config",
			event.ClockIDKey:            "001122.fffe.334455",
			event.OFFSET:                int64(10),
		}, false)
	time.Sleep(100 * time.Millisecond)

	for i := 0; i < 10; i++ {
		eChannel <- sendBCEvent(bcCfgPTP4l, event.PTP4l, event.PTP_LOCKED,
			map[event.ValueType]interface{}{event.OFFSET: int64(10)}, false)
		time.Sleep(50 * time.Millisecond)
	}

	// Trigger "stay LOCKED" → upstream data mismatch → announceClockClass(6)
	eChannel <- sendBCEvent(bcCfgDPLL, event.DPLL, event.PTP_LOCKED,
		map[event.ValueType]interface{}{event.OFFSET: int64(10)}, false)

	assert.True(t, waitForMetric(clockClassGauge, bcCfgPTP4l, 6, 5*time.Second),
		"expected clock class metric = 6 for %s after LOCKED", bcCfgPTP4l)

	// PTP4l source lost → LOCKED→HOLDOVER (class 135)
	eChannel <- sendBCEvent(bcCfgPTP4l, event.PTP4l, event.PTP_FREERUN,
		map[event.ValueType]interface{}{event.OFFSET: int64(10)}, true)

	assert.True(t, waitForMetric(clockClassGauge, bcCfgPTP4l, 135, 5*time.Second),
		"expected clock class metric = 135 for %s after HOLDOVER", bcCfgPTP4l)
}

// drainIPCMessages collects all IPC messages available on the channel within the timeout.
func drainIPCMessages(ch <-chan ipc.Message, timeout time.Duration) []ipc.Message {
	var msgs []ipc.Message
	deadline := time.After(timeout)
	for {
		select {
		case msg := <-ch:
			msgs = append(msgs, msg)
		case <-deadline:
			return msgs
		}
	}
}

// waitForIPCMessage waits for an IPC message matching the given type and returns it.
func waitForIPCMessage(ch <-chan ipc.Message, msgType string, timeout time.Duration) (ipc.Message, bool) {
	deadline := time.After(timeout)
	for {
		select {
		case msg := <-ch:
			if msg.Type == msgType {
				return msg, true
			}
		case <-deadline:
			return ipc.Message{}, false
		}
	}
}

func TestBCClockIPCCacheIntegration(t *testing.T) {
	pmcMock := &pmc.MockClient{
		ParentTimeCurrentDSResult: pmc.ParentTimeCurrentDS{
			ParentDataSet: protocol.ParentDataSet{
				GrandmasterClockClass:    6,
				GrandmasterClockAccuracy: 0x21,
			},
			TimePropertiesDS: protocol.TimePropertiesDS{
				CurrentUtcOffset:      37,
				CurrentUtcOffsetValid: true,
				PtpTimescale:          true,
				TimeTraceable:         true,
			},
			CurrentDS: protocol.CurrentDS{StepsRemoved: 1},
		},
	}
	pmc.SetMock(pmcMock)
	defer pmc.ResetMock()

	assert.NoError(t, leap.MockLeapFile())
	defer func() {
		close(leap.LeapMgr.Close)
		time.Sleep(100 * time.Millisecond)
	}()

	clockClassGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "test_ipc_clock_class", Help: "test"},
		[]string{"process", "config", "node"},
	)
	offsetGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "test_ipc_offset", Help: "test"},
		[]string{"from", "process", "node", "iface"},
	)
	clockStateGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "test_ipc_clock_state", Help: "test"},
		[]string{"process", "node", "iface"},
	)

	cache := ipc.NewCache(100)
	eChannel := make(chan event.Event, 100)
	closeChn := make(chan bool)
	eventManager := event.Init("node", false, "", eChannel, closeChn, offsetGauge, clockStateGauge, clockClassGauge, cache)
	go eventManager.ProcessEvents()
	defer func() { closeChn <- true }()

	clk, addErr := eventManager.AddClock("ptp4l.0.config", event.BC)
	require.NoError(t, addErr)
	bc := clk.(*event.BCClock)
	bc.UpdateUpstreamParentDataSet(protocol.ParentDataSet{
		GrandmasterClockClass:    6,
		GrandmasterClockAccuracy: 0x21,
	})

	time.Sleep(200 * time.Millisecond)

	const (
		bcCfgDPLL  = "ts2phc.0.config"
		bcCfgPTP4l = "ptp4l.0.config"
	)

	// --- Phase 1: FREERUN → LOCKED ---

	// First DPLL event carries leading source configuration
	eChannel <- sendBCEvent(bcCfgDPLL, event.DPLL, event.PTP_LOCKED,
		map[event.ValueType]interface{}{
			event.LeadingSource:            true,
			event.InSyncConditionThreshold: uint64(10000),
			event.InSyncConditionTimes:     uint64(1),
			event.ToFreeRunThreshold:       uint64(1500),
			event.MaxInSpecOffset:          uint64(500),
			event.OFFSET:                   int64(10),
		}, false)
	time.Sleep(100 * time.Millisecond)

	// Fill DPLL window (10 events for WindowSize=10)
	for i := 0; i < 10; i++ {
		eChannel <- sendBCEvent(bcCfgDPLL, event.DPLL, event.PTP_LOCKED,
			map[event.ValueType]interface{}{event.OFFSET: int64(10)}, false)
		time.Sleep(50 * time.Millisecond)
	}

	// First PTP4l event with downstream config
	eChannel <- sendBCEvent(bcCfgPTP4l, event.PTP4l, event.PTP_LOCKED,
		map[event.ValueType]interface{}{
			event.ControlledPortsConfig: "ptp4l.0.config",
			event.ClockIDKey:            "001122.fffe.334455",
			event.OFFSET:                int64(10),
		}, false)
	time.Sleep(100 * time.Millisecond)

	// Fill PTP4l window — last event triggers FREERUN→LOCKED
	for i := 0; i < 10; i++ {
		eChannel <- sendBCEvent(bcCfgPTP4l, event.PTP4l, event.PTP_LOCKED,
			map[event.ValueType]interface{}{event.OFFSET: int64(10)}, false)
		time.Sleep(50 * time.Millisecond)
	}

	// Collect IPC messages from FREERUN→LOCKED transition
	msgs := drainIPCMessages(cache.Out(), 2*time.Second)

	var gotPTPStateLocked bool
	for _, msg := range msgs {
		if msg.Type == ipc.TypePTPState {
			sv, ok := msg.Values.(ipc.StateValue)
			if ok && sv.State == ipc.StateLocked {
				gotPTPStateLocked = true
				assert.Equal(t, bcCfgPTP4l, msg.Profile, "ptp_state profile should be ptp4l config")
				assert.Equal(t, "ens1f0", msg.IFace, "ptp_state iface should be leading interface")
			}
		}
	}
	assert.True(t, gotPTPStateLocked, "expected ptp_state LOCKED after FREERUN→LOCKED transition")

	// --- Phase 2: LOCKED → HOLDOVER ---

	// Drain any leftover messages before triggering the next transition
	drainIPCMessages(cache.Out(), 200*time.Millisecond)

	// PTP4l source lost → LOCKED→HOLDOVER
	eChannel <- sendBCEvent(bcCfgPTP4l, event.PTP4l, event.PTP_FREERUN,
		map[event.ValueType]interface{}{event.OFFSET: int64(10)}, true)

	msgs = drainIPCMessages(cache.Out(), 2*time.Second)

	var gotPTPStateHoldover, gotClockClass135 bool
	for _, msg := range msgs {
		if msg.Type == ipc.TypePTPState {
			sv, ok := msg.Values.(ipc.StateValue)
			if ok && sv.State == ipc.StateHoldover {
				gotPTPStateHoldover = true
			}
		}
		if msg.Type == ipc.TypeClockClass {
			cv, ok := msg.Values.(ipc.ClockClassValue)
			if ok && cv.ClockClass == 135 {
				gotClockClass135 = true
			}
		}
	}
	assert.True(t, gotPTPStateHoldover, "expected ptp_state HOLDOVER after LOCKED→HOLDOVER")
	assert.True(t, gotClockClass135, "expected clock_class 135 after LOCKED→HOLDOVER")

	// --- Phase 3: HOLDOVER → FREERUN ---

	drainIPCMessages(cache.Out(), 200*time.Millisecond)

	// Large offset on leading DPLL triggers freeRunCondition → HOLDOVER→FREERUN
	eChannel <- sendBCEvent(bcCfgDPLL, event.DPLL, event.PTP_LOCKED,
		map[event.ValueType]interface{}{event.OFFSET: int64(50000)}, false)

	msgs = drainIPCMessages(cache.Out(), 2*time.Second)

	var gotPTPStateFreerun, gotClockClass248 bool
	for _, msg := range msgs {
		if msg.Type == ipc.TypePTPState {
			sv, ok := msg.Values.(ipc.StateValue)
			if ok && sv.State == ipc.StateFreerun {
				gotPTPStateFreerun = true
			}
		}
		if msg.Type == ipc.TypeClockClass {
			cv, ok := msg.Values.(ipc.ClockClassValue)
			if ok && cv.ClockClass == 248 {
				gotClockClass248 = true
			}
		}
	}
	assert.True(t, gotPTPStateFreerun, "expected ptp_state FREERUN after HOLDOVER→FREERUN")
	assert.True(t, gotClockClass248, "expected clock_class 248 after HOLDOVER→FREERUN")

	// --- Phase 4: Dedup check ---

	drainIPCMessages(cache.Out(), 200*time.Millisecond)

	// Send more events that don't change state — should produce no IPC messages
	for i := 0; i < 5; i++ {
		eChannel <- sendBCEvent(bcCfgDPLL, event.DPLL, event.PTP_LOCKED,
			map[event.ValueType]interface{}{event.OFFSET: int64(50000)}, false)
		time.Sleep(50 * time.Millisecond)
	}

	dedup := drainIPCMessages(cache.Out(), 500*time.Millisecond)
	assert.Empty(t, dedup, "duplicate events should not produce IPC messages")

	// --- Verify cache snapshot ---

	snap := cache.Snapshot()
	assert.NotEmpty(t, snap, "cache snapshot should contain stored messages")
	types := map[string]bool{}
	for _, m := range snap {
		types[m.Type] = true
	}
	assert.True(t, types[ipc.TypePTPState], "snapshot should contain ptp_state")
	assert.True(t, types[ipc.TypeClockClass], "snapshot should contain clock_class")
}

func TestOverallClockStateIntegration(t *testing.T) {
	pmcMock := &pmc.MockClient{
		ParentTimeCurrentDSResult: pmc.ParentTimeCurrentDS{
			ParentDataSet: protocol.ParentDataSet{
				GrandmasterClockClass:    6,
				GrandmasterClockAccuracy: 0x21,
			},
			TimePropertiesDS: protocol.TimePropertiesDS{
				CurrentUtcOffset:      37,
				CurrentUtcOffsetValid: true,
				PtpTimescale:          true,
				TimeTraceable:         true,
			},
			CurrentDS: protocol.CurrentDS{StepsRemoved: 1},
		},
	}
	pmc.SetMock(pmcMock)
	defer pmc.ResetMock()

	assert.NoError(t, leap.MockLeapFile())
	defer func() {
		close(leap.LeapMgr.Close)
		time.Sleep(100 * time.Millisecond)
	}()

	clockClassGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "test_overall_clock_class", Help: "test"},
		[]string{"process", "config", "node"},
	)
	offsetGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "test_overall_offset", Help: "test"},
		[]string{"from", "process", "node", "iface"},
	)
	clockStateGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "test_overall_clock_state", Help: "test"},
		[]string{"process", "node", "iface"},
	)

	cache := ipc.NewCache(100)
	eChannel := make(chan event.Event, 100)
	closeChn := make(chan bool)
	eventManager := event.Init("node", false, "", eChannel, closeChn, offsetGauge, clockStateGauge, clockClassGauge, cache)
	go eventManager.ProcessEvents()
	defer func() { closeChn <- true }()

	// Register two BCClocks
	clk1, err := eventManager.AddClock("ptp4l.0.config", event.BC)
	require.NoError(t, err)
	bc1 := clk1.(*event.BCClock)
	bc1.UpdateUpstreamParentDataSet(protocol.ParentDataSet{
		GrandmasterClockClass:    6,
		GrandmasterClockAccuracy: 0x21,
	})

	clk2, err := eventManager.AddClock("ptp4l.1.config", event.BC)
	require.NoError(t, err)
	bc2 := clk2.(*event.BCClock)
	bc2.UpdateUpstreamParentDataSet(protocol.ParentDataSet{
		GrandmasterClockClass:    6,
		GrandmasterClockAccuracy: 0x21,
	})

	time.Sleep(200 * time.Millisecond)

	// --- Phase 1: Lock both BCClocks' PTP state ---

	lockBCClock := func(cfgDPLL, cfgPTP4l string) {
		eChannel <- sendBCEvent(cfgDPLL, event.DPLL, event.PTP_LOCKED,
			map[event.ValueType]interface{}{
				event.LeadingSource:            true,
				event.InSyncConditionThreshold: uint64(10000),
				event.InSyncConditionTimes:     uint64(1),
				event.ToFreeRunThreshold:       uint64(1500),
				event.MaxInSpecOffset:          uint64(500),
				event.OFFSET:                   int64(10),
			}, false)
		time.Sleep(100 * time.Millisecond)

		for i := 0; i < 10; i++ {
			eChannel <- sendBCEvent(cfgDPLL, event.DPLL, event.PTP_LOCKED,
				map[event.ValueType]interface{}{event.OFFSET: int64(10)}, false)
			time.Sleep(50 * time.Millisecond)
		}

		eChannel <- sendBCEvent(cfgPTP4l, event.PTP4l, event.PTP_LOCKED,
			map[event.ValueType]interface{}{
				event.ControlledPortsConfig: cfgPTP4l,
				event.ClockIDKey:            "001122.fffe.334455",
				event.OFFSET:                int64(10),
			}, false)
		time.Sleep(100 * time.Millisecond)

		for i := 0; i < 10; i++ {
			eChannel <- sendBCEvent(cfgPTP4l, event.PTP4l, event.PTP_LOCKED,
				map[event.ValueType]interface{}{event.OFFSET: int64(10)}, false)
			time.Sleep(50 * time.Millisecond)
		}
	}

	lockBCClock("ts2phc.0.config", "ptp4l.0.config")
	lockBCClock("ts2phc.1.config", "ptp4l.1.config")

	// Drain all messages from PTP locking phase
	msgs := drainIPCMessages(cache.Out(), 2*time.Second)

	// Verify we got ptp_state LOCKED but sync_state should be FREERUN
	// (os clock is still FREERUN since no PHC2SYS event yet)
	var gotPTPLocked int
	var gotSyncStateFreerun int
	for _, msg := range msgs {
		if msg.Type == ipc.TypePTPState {
			if sv, ok := msg.Values.(ipc.StateValue); ok && sv.State == ipc.StateLocked {
				gotPTPLocked++
			}
		}
		if msg.Type == ipc.TypeSyncState {
			if sv, ok := msg.Values.(ipc.SyncStateValue); ok && sv.State == ipc.StateFreerun {
				gotSyncStateFreerun++
			}
		}
	}
	assert.GreaterOrEqual(t, gotPTPLocked, 2, "expected ptp_state LOCKED for both profiles")

	// --- Phase 2: Send PHC2SYS LOCKED event ---

	drainIPCMessages(cache.Out(), 200*time.Millisecond)

	eChannel <- event.Event{
		Source:    event.PHC2SYS,
		IFace:     "CLOCK_REALTIME",
		CfgName:   "ptp4l.0.config",
		ClockType: event.BC,
		Time:      time.Now().UnixMilli(),
		Data: &event.PTPData{
			State:  event.PTP_LOCKED,
			Values: map[event.ValueType]interface{}{event.OFFSET: int64(5)},
		},
	}

	msgs = drainIPCMessages(cache.Out(), 2*time.Second)

	var osClockCount int
	var syncStateLocked int
	syncStateProfiles := map[string]bool{}
	for _, msg := range msgs {
		if msg.Type == ipc.TypeOSClockState {
			osClockCount++
			sv, ok := msg.Values.(ipc.StateValue)
			require.True(t, ok)
			assert.Equal(t, ipc.StateLocked, sv.State)
		}
		if msg.Type == ipc.TypeSyncState {
			sv, ok := msg.Values.(ipc.SyncStateValue)
			if ok && sv.State == ipc.StateLocked {
				syncStateLocked++
				syncStateProfiles[msg.Profile] = true
			}
		}
	}
	assert.Equal(t, 1, osClockCount, "os_clock_state should be emitted exactly once")
	assert.Equal(t, 2, syncStateLocked, "sync_state LOCKED should be emitted for both profiles")
	assert.True(t, syncStateProfiles["ptp4l.0.config"], "sync_state should include profile 0")
	assert.True(t, syncStateProfiles["ptp4l.1.config"], "sync_state should include profile 1")

	// --- Phase 3: Send PHC2SYS FREERUN → overall drops to FREERUN ---

	drainIPCMessages(cache.Out(), 200*time.Millisecond)

	eChannel <- event.Event{
		Source:    event.PHC2SYS,
		IFace:     "CLOCK_REALTIME",
		CfgName:   "ptp4l.0.config",
		ClockType: event.BC,
		Time:      time.Now().UnixMilli(),
		Data: &event.PTPData{
			State:  event.PTP_FREERUN,
			Values: map[event.ValueType]interface{}{event.OFFSET: int64(0)},
		},
	}

	msgs = drainIPCMessages(cache.Out(), 2*time.Second)

	var syncStateFreerunCount int
	for _, msg := range msgs {
		if msg.Type == ipc.TypeSyncState {
			sv, ok := msg.Values.(ipc.SyncStateValue)
			if ok && sv.State == ipc.StateFreerun {
				syncStateFreerunCount++
			}
		}
	}
	assert.Equal(t, 2, syncStateFreerunCount, "sync_state FREERUN should be emitted for both profiles")

	// --- Phase 4: Duplicate PHC2SYS FREERUN → no new messages ---

	drainIPCMessages(cache.Out(), 200*time.Millisecond)

	eChannel <- event.Event{
		Source:    event.PHC2SYS,
		IFace:     "CLOCK_REALTIME",
		CfgName:   "ptp4l.0.config",
		ClockType: event.BC,
		Time:      time.Now().UnixMilli(),
		Data: &event.PTPData{
			State:  event.PTP_FREERUN,
			Values: map[event.ValueType]interface{}{event.OFFSET: int64(0)},
		},
	}

	dedup := drainIPCMessages(cache.Out(), 500*time.Millisecond)
	osClockDedup := 0
	syncStateDedup := 0
	for _, msg := range dedup {
		if msg.Type == ipc.TypeOSClockState {
			osClockDedup++
		}
		if msg.Type == ipc.TypeSyncState {
			syncStateDedup++
		}
	}
	assert.Equal(t, 0, osClockDedup, "duplicate os_clock_state should not be emitted")
	assert.Equal(t, 0, syncStateDedup, "duplicate sync_state should not be emitted")
}

func TestSyncEIPCIntegration(t *testing.T) {
	pmcMock := &pmc.MockClient{
		ParentTimeCurrentDSResult: pmc.ParentTimeCurrentDS{
			ParentDataSet: protocol.ParentDataSet{
				GrandmasterClockClass:    6,
				GrandmasterClockAccuracy: 0x21,
			},
			TimePropertiesDS: protocol.TimePropertiesDS{
				CurrentUtcOffset:      37,
				CurrentUtcOffsetValid: true,
				PtpTimescale:          true,
				TimeTraceable:         true,
			},
			CurrentDS: protocol.CurrentDS{StepsRemoved: 1},
		},
	}
	pmc.SetMock(pmcMock)
	defer pmc.ResetMock()

	assert.NoError(t, leap.MockLeapFile())
	defer func() {
		close(leap.LeapMgr.Close)
		time.Sleep(100 * time.Millisecond)
	}()

	clockClassGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "test_synce_clock_class", Help: "test"},
		[]string{"process", "config", "node"},
	)
	offsetGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "test_synce_offset", Help: "test"},
		[]string{"from", "process", "node", "iface"},
	)
	clockStateGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "test_synce_clock_state", Help: "test"},
		[]string{"process", "node", "iface"},
	)

	cache := ipc.NewCache(100)
	eChannel := make(chan event.Event, 100)
	closeChn := make(chan bool)
	eventManager := event.Init("node", false, "", eChannel, closeChn, offsetGauge, clockStateGauge, clockClassGauge, cache)
	go eventManager.ProcessEvents()
	defer func() { closeChn <- true }()

	_, err := eventManager.AddClock("ptp4l.0.config", event.BC)
	require.NoError(t, err)

	time.Sleep(200 * time.Millisecond)

	// --- Phase 1: SyncE state event → synce_state IPC ---

	eChannel <- event.Event{
		Source:     event.SYNCE,
		IFace:      "ens7f0",
		CfgName:    "synce4l.0.config",
		Time:       time.Now().UnixMilli(),
		WriteToLog: true,
		Data: &event.PTPData{
			State: event.PTP_LOCKED,
			Values: map[event.ValueType]interface{}{
				event.EEC_STATE:      "EEC_LOCKED",
				event.DEVICE:         "synce1",
				event.NETWORK_OPTION: 1,
			},
		},
	}

	msg, ok := waitForIPCMessage(cache.Out(), ipc.TypeSyncEState, 2*time.Second)
	assert.True(t, ok, "expected synce_state IPC message")
	if ok {
		assert.Equal(t, "ptp4l.0.config", msg.Profile)
		assert.Equal(t, "ens7f0", msg.IFace)
		sv, svOK := msg.Values.(ipc.SyncEStateValue)
		require.True(t, svOK)
		assert.Equal(t, "EEC_LOCKED", sv.State)
	}

	// --- Phase 2: SyncE clock quality event → synce_clock_quality IPC ---

	drainIPCMessages(cache.Out(), 200*time.Millisecond)

	eChannel <- event.Event{
		Source:     event.SYNCE,
		IFace:      "ens7f0",
		CfgName:    "synce4l.0.config",
		Time:       time.Now().UnixMilli(),
		WriteToLog: true,
		Data: &event.PTPData{
			State: event.PTP_LOCKED,
			Values: map[event.ValueType]interface{}{
				event.QL:             byte(4),
				event.EXT_QL:         byte(0xFF),
				event.CLOCK_QUALITY:  "PRS",
				event.DEVICE:         "synce1",
				event.NETWORK_OPTION: 1,
			},
		},
	}

	msg, ok = waitForIPCMessage(cache.Out(), ipc.TypeSyncEClockQuality, 2*time.Second)
	assert.True(t, ok, "expected synce_clock_quality IPC message")
	if ok {
		assert.Equal(t, "ptp4l.0.config", msg.Profile)
		qv, qvOK := msg.Values.(ipc.SyncEClockQualityValue)
		require.True(t, qvOK)
		assert.Equal(t, 4, qv.QL)
		assert.Equal(t, 0xFF, qv.ExtendedQL)
	}

	// --- Phase 3: Verify SyncE does NOT affect ptp_state or sync_state ---

	drainIPCMessages(cache.Out(), 200*time.Millisecond)

	eChannel <- event.Event{
		Source:     event.SYNCE,
		IFace:      "ens7f0",
		CfgName:    "synce4l.0.config",
		Time:       time.Now().UnixMilli(),
		WriteToLog: true,
		Data: &event.PTPData{
			State: event.PTP_FREERUN,
			Values: map[event.ValueType]interface{}{
				event.EEC_STATE:      "EEC_FREERUN",
				event.DEVICE:         "synce1",
				event.NETWORK_OPTION: 1,
			},
		},
	}

	msgs := drainIPCMessages(cache.Out(), 1*time.Second)
	for _, m := range msgs {
		assert.NotEqual(t, ipc.TypePTPState, m.Type, "SyncE should not produce ptp_state IPC")
		assert.NotEqual(t, ipc.TypeSyncState, m.Type, "SyncE should not produce sync_state IPC")
	}

	// Verify synce_state was updated to FREERUN
	snap := cache.Snapshot()
	var foundSyncEState bool
	for _, m := range snap {
		if m.Type == ipc.TypeSyncEState {
			foundSyncEState = true
			sv, svOK := m.Values.(ipc.SyncEStateValue)
			require.True(t, svOK)
			assert.Equal(t, "EEC_FREERUN", sv.State)
		}
	}
	assert.True(t, foundSyncEState, "cache should contain synce_state after SyncE event")
}
