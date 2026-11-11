package daemon

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type ts2phcLogCmd struct{ *MockCmd }

func (c *ts2phcLogCmd) Clone() ProcessCmd { return c }

func (c *ts2phcLogCmd) StdoutPipe() (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("ts2phc[2000.100]: [ts2phc.1.config:7] adding tstamp 10.000000000 to clock /dev/ptp1\n")), nil
}

func newTS2PHCObservationTestProcess() *ptpProcess {
	InitializeOffsetMaps()
	return &ptpProcess{
		ExecProcess: ExecProcess{
			name: ts2phcProcessName, configName: "ts2phc.1.config",
			messageTag: "[ts2phc.1.config]", eventCh: make(chan event.Event, 64),
		},
		ifaces: config.IFaces{
			{Name: "ens0", PhcId: "/dev/ptp0", Source: event.PPS},
			{Name: "ens1", PhcId: "/dev/ptp1", Source: event.PPS},
			{Name: "ens2", PhcId: "/dev/ptp2", Source: event.PPS},
		},
		logParser: parser.NewTS2PHCExtractor(), clockType: event.TBC,
		nodeProfile:       &ptpv1.PtpProfile{PtpSettings: map[string]string{"leadingInterface": "ens0"}},
		ptpClockThreshold: &ptpv1.PtpClockThreshold{MaxOffsetThreshold: 10000},
	}
}

func feedTS2PHCPollLine(p *ptpProcess, timestamp, message string) {
	processWithParser(p, fmt.Sprintf("ts2phc[%s]: [ts2phc.1.config:7] %s", timestamp, message))
}

func TestTS2PHCObservationInterfaceResolution(t *testing.T) {
	for _, tt := range []struct {
		name           string
		message        string
		iface          string
		device         string
		role           event.TS2PHCRole
		timeout        bool
		pollSuccessful bool
		offset         *float64
	}{
		{
			name: "interface offset", message: "ens1 offset 42 s2 freq +0",
			iface: "ens1", pollSuccessful: true, offset: utils.ToPtr(float64(42)),
		},
		{
			name: "PHC offset", message: "/dev/ptp1 offset 42 s2 freq +0",
			iface: "ens1", pollSuccessful: true, offset: utils.ToPtr(float64(42)),
		},
		{
			name: "unknown PHC offset", message: "/dev/ptp99 offset 42 s2 freq +0",
			iface: "/dev/ptp99", pollSuccessful: true, offset: utils.ToPtr(float64(42)),
		},
		{
			name: "NMEA metric only", message: "ens1 nmea_status 1 offset 0 s2",
			iface: "ens1", offset: utils.ToPtr(float64(0)),
		},
		{
			name: "timestamp", message: "adding tstamp 10.000000000 to clock /dev/ptp1",
			iface: "ens1", device: "/dev/ptp1",
		},
		{
			name: "unknown timestamp", message: "adding tstamp 10.000000000 to clock /dev/ptp99",
			iface: "/dev/ptp99", device: "/dev/ptp99",
		},
		{
			name: "selection", message: "selecting /dev/ptp1 for synchronization",
			iface: "ens1", device: "/dev/ptp1", role: event.TS2PHCSink,
		},
		{
			name: "timeout has no interface", message: "poll returns zero, no events", timeout: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := newTS2PHCObservationTestProcess()
			// An empty PHC ID must not turn a timeout into an interface event.
			p.ifaces = append(config.IFaces{{Name: "unresolved"}}, p.ifaces...)
			feedTS2PHCPollLine(p, "2000.100", tt.message)
			events := drainEvents(p.eventCh)
			require.Len(t, events, 1)
			assert.Equal(t, tt.iface, events[0].IFace)
			require.IsType(t, &event.TS2PHCObservation{}, events[0].Data)
			observation := events[0].Data.(*event.TS2PHCObservation)
			assert.Equal(t, tt.device, observation.PHCDevice)
			assert.Equal(t, tt.role, observation.Role)
			assert.Equal(t, tt.timeout, observation.PollTimeout)
			assert.Equal(t, tt.pollSuccessful, observation.PollSuccessful)
			assert.Equal(t, tt.offset, observation.Offset)
		})
	}
}

func TestTS2PHCObservationInterfacePrecedence(t *testing.T) {
	for _, tt := range []struct {
		name   string
		metric *parser.Metrics
		parsed *parser.PTPEvent
		want   string
	}{
		{
			name:   "metric before event interface and PHC",
			metric: &parser.Metrics{Iface: "/dev/ptp2", Offset: 42, Source: constants.Master, ClockState: constants.ClockStateLocked},
			parsed: &parser.PTPEvent{Iface: "/dev/ptp1", Ts2Phc: &parser.Ts2PhcEvent{PHCDevice: "/dev/ptp0", PollSuccessful: true, Holdover: true}},
			want:   "ens2",
		},
		{
			name:   "event interface before PHC",
			parsed: &parser.PTPEvent{Iface: "/dev/ptp1", Ts2Phc: &parser.Ts2PhcEvent{PHCDevice: "/dev/ptp0"}},
			want:   "ens1",
		},
		{
			name:   "empty metric interface uses event",
			metric: &parser.Metrics{Offset: 42, Source: constants.Master, ClockState: constants.ClockStateLocked},
			parsed: &parser.PTPEvent{Iface: "/dev/ptp1", Ts2Phc: &parser.Ts2PhcEvent{PollSuccessful: true}},
			want:   "ens1",
		},
		{
			name:   "empty identifiers stay empty",
			parsed: &parser.PTPEvent{Ts2Phc: &parser.Ts2PhcEvent{PollTimeout: true}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := newTS2PHCObservationTestProcess()
			p.ifaces = append(config.IFaces{{Name: "unresolved"}}, p.ifaces...)
			p.processTS2PHCOutput(tt.metric, tt.parsed)
			events := drainEvents(p.eventCh)
			require.Len(t, events, 1)
			assert.Equal(t, tt.want, events[0].IFace)
			require.IsType(t, &event.TS2PHCObservation{}, events[0].Data)
			observation := events[0].Data.(*event.TS2PHCObservation)
			if tt.parsed != nil && tt.parsed.Ts2Phc != nil {
				assert.Equal(t, tt.parsed.Ts2Phc.PHCDevice, observation.PHCDevice)
				assert.Equal(t, tt.parsed.Ts2Phc.PollSuccessful, observation.PollSuccessful)
				assert.Equal(t, tt.parsed.Ts2Phc.Holdover, observation.Holdover)
			}
			if tt.metric != nil {
				require.NotNil(t, observation.Offset, "metric and event facts must both be preserved")
				assert.Equal(t, tt.metric.Offset, *observation.Offset)
			}
		})
	}
}

func TestTS2PHCDaemonForwardsFactsWithoutPolicy(t *testing.T) {
	for _, clockType := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(clockType), func(t *testing.T) {
			p := newTS2PHCObservationTestProcess()
			p.clockType = clockType
			p.forwardTS2PHCEvent("", &event.TS2PHCRunStarted{})
			feedTS2PHCPollLine(p, "2000.100", "adding tstamp 1791217457.000000001 to clock /dev/ptp0")
			feedTS2PHCPollLine(p, "2000.200", "selecting /dev/ptp0 for synchronization")
			feedTS2PHCPollLine(p, "2000.300", "ens0 offset 10000 s2 freq +0")
			feedTS2PHCPollLine(p, "2000.400", "poll returns zero, no events")
			feedTS2PHCPollLine(p, "2001.100", "adding tstamp 1791217458.000000001 to clock /dev/ptp0")
			events := drainEvents(p.eventCh)
			require.Len(t, events, 6, "one observation per line plus the run-start signal")
			for _, ev := range events {
				assert.Equal(t, event.TS2PHC, ev.Source)
				assert.Equal(t, "ts2phc.1.config", ev.CfgName)
				assert.Equal(t, clockType, ev.ClockType)
			}
			require.IsType(t, &event.TS2PHCRunStarted{}, events[0].Data)
			for _, ev := range events[1:] {
				require.IsType(t, &event.TS2PHCObservation{}, ev.Data)
			}
			cfg := p.ts2phcConfig()
			assert.Equal(t, "ens0", cfg.LeadingInterface)
			require.Len(t, cfg.Interfaces, 3)
			assert.Equal(t, "/dev/ptp0", cfg.Interfaces[0].PHCDevice)
			assert.Equal(t, int64(10000), cfg.Threshold.MaxOffsetThreshold)
			stamp := events[1].Data.(*event.TS2PHCObservation)
			assert.Equal(t, "ens0", events[1].IFace)
			assert.Equal(t, int64(1), stamp.PHCTstamp.Nanoseconds)
			offset := events[3].Data.(*event.TS2PHCObservation)
			assert.True(t, offset.PollSuccessful)
			assert.Equal(t, event.PTP_LOCKED, offset.State, "threshold decisions must not happen in the daemon")
			assert.True(t, events[4].Data.(*event.TS2PHCObservation).PollTimeout, "timeouts must reach both clock types")
			p.ifaces[0].Name = "changed"
			assert.Equal(t, "ens0", cfg.Interfaces[0].Name, "setup configuration is detached from the process")
		})
	}
}

func TestTS2PHCProfileSetupConfiguresClockBeforeProcessRun(t *testing.T) {
	for _, clockType := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(clockType), func(t *testing.T) {
			InitializeOffsetMaps()
			name := "test-profile"
			configText := "[global]\n[ens0]\nts2phc.master 0\n[ens1]\nts2phc.master 0\n"
			opts := "-m"
			profile := &ptpv1.PtpProfile{
				Name: &name, Ts2PhcConf: &configText, Ts2PhcOpts: &opts,
				PtpSettings:       map[string]string{"leadingInterface": "ens0"},
				PtpClockThreshold: &ptpv1.PtpClockThreshold{MaxOffsetThreshold: 10000},
			}
			ct := NewCondiitonsTester(t, clockType, "ptp4l.0.config")
			dn := NewDaemonForTests(&ReadyTracker{}, ct.pm)
			t.Cleanup(dn.cancel)
			topology, err := NewProfileTopology([]ptpv1.PtpProfile{*profile})
			require.NoError(t, err)
			require.NoError(t, dn.createProcesses(0, profile, &topology, clockType, "ens0", nil))
			processes := dn.processManager.findProcessesByName(ts2phcProcessName)
			require.Len(t, processes, 1)
			proc := processes[0].(*ptpProcess)
			require.Len(t, proc.ifaces, 2, "the existing rendering path resolves the process configuration")
			clk := dn.processManager.clockMgr.GetClock(proc.configName)
			assert.Equal(t, event.PTP_NOTSET, clk.GetState(), "profile configuration is not a runtime clock observation")
			// T-BC exposes the configured leader on its first DPLL evaluation,
			// even when macOS cannot resolve actual hardware PHCs.
			if clockType == event.TBC {
				state := clk.AddEvent(event.Event{Source: event.DPLL, IFace: "ens0", Time: time.Now().UnixMilli(), Data: &event.DPLLData{State: event.PTP_LOCKED}})
				assert.Equal(t, "ens0", state.LeadingIFace)
			}
		})
	}
}

func TestTS2PHCObservationDistinguishesNMEAFromServoOffset(t *testing.T) {
	p := newTS2PHCObservationTestProcess()
	feedTS2PHCPollLine(p, "2000.100", "ens0 nmea_status 1 offset 0 s2")
	feedTS2PHCPollLine(p, "2000.200", "ens0 offset 0 s3 freq +0 holdover")
	events := drainEvents(p.eventCh)
	require.Len(t, events, 2)
	assert.False(t, events[0].Data.(*event.TS2PHCObservation).PollSuccessful)
	assert.True(t, events[1].Data.(*event.TS2PHCObservation).PollSuccessful)
	assert.True(t, events[1].Data.(*event.TS2PHCObservation).Holdover)
	feedTS2PHCPollLine(p, "2000.300", "source ts not valid")
	assert.Empty(t, drainEvents(p.eventCh), "unrelated parser counters are not fake offset observations")
	parser.NoSourceTSCount = 0
}

func TestTS2PHCLegacyPathAlsoForwardsRawState(t *testing.T) {
	p := newTS2PHCObservationTestProcess()
	p.processTs2PhcEvents(10000, ts2phcProcessName, "ens1", event.PTP_LOCKED, nil)
	events := drainEvents(p.eventCh)
	require.Len(t, events, 1)
	require.IsType(t, &event.TS2PHCObservation{}, events[0].Data)
	assert.Equal(t, event.PTP_LOCKED, events[0].Data.(*event.TS2PHCObservation).State)
	assert.True(t, events[0].Data.(*event.TS2PHCObservation).PollSuccessful)
}

func TestTS2PHCObservationBackpressureCanBeStopped(t *testing.T) {
	p := newTS2PHCObservationTestProcess()
	p.eventCh = make(chan event.Event)
	p.stopCh = make(chan struct{})
	done := make(chan struct{})
	go func() {
		p.forwardTS2PHCEvent("", &event.TS2PHCRunStarted{})
		close(done)
	}()
	close(p.stopCh)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stopping must unblock observation forwarding")
	}
}

func TestTS2PHCProcessDownRequestsConfigMetricCleanup(t *testing.T) {
	p := newTS2PHCObservationTestProcess()
	p.updateGMStatusOnProcessDown(ts2phcProcessName)
	events := drainEvents(p.eventCh)
	require.Len(t, events, 1)
	assert.Equal(t, event.TS2PHC, events[0].Source)
	assert.Equal(t, p.configName, events[0].CfgName)
	assert.True(t, events[0].Reset)
	assert.True(t, events[0].ResetAllMetrics)
}

func TestTS2PHCProcessRunBoundaryPrecedesScannedObservations(t *testing.T) {
	p := newTS2PHCObservationTestProcess()
	p.cmd = &ts2phcLogCmd{MockCmd: NewMockCmd()}
	p.pm = &plugin.PluginManager{}
	p.state = process.Created
	p.stopCh = make(chan struct{})
	p.run()
	t.Cleanup(func() { _ = p.Stop() })
	for _, expected := range []struct {
		name string
		kind event.EventData
	}{
		{"process status", &event.ProcessStatusData{}},
		{"run boundary", &event.TS2PHCRunStarted{}},
		{"timestamp observation", &event.TS2PHCObservation{}},
	} {
		select {
		case ev := <-p.eventCh:
			require.IsType(t, expected.kind, ev.Data, expected.name)
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", expected.name)
		}
	}
}
