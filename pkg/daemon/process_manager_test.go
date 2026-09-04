package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clockmgr"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
)

const (
	testETH0          = "eth0"
	testE810          = "e810"
	testGNSSFailover  = "gnss_failover"
	testGNSSRecovered = "gnss_recovered"
	testPtp4l1Config  = "ptp4l.1.config"
)

type stubProcess struct {
	name    string
	iface   string
	state   process.State
	starts  int
	deps    []process.Process
	conds   map[process.Action]process.Condition
	profile *ptpv1.PtpProfile
}

func (s *stubProcess) Name() string { return s.name }
func (s *stubProcess) Start(context.Context) error {
	s.starts++
	s.state = process.Running
	return nil
}
func (s *stubProcess) Stop() error { return nil }
func (s *stubProcess) Conditions() map[process.Action]process.Condition {
	return s.conds
}
func (s *stubProcess) State() process.State       { return s.state }
func (s *stubProcess) Profile() *ptpv1.PtpProfile { return s.profile }
func (s *stubProcess) ClockType() event.ClockType { return event.OC }
func (s *stubProcess) DependentProcesses() []process.Process {
	return s.deps
}
func (s *stubProcess) IFace() string { return s.iface }

func testClockMgr(t *testing.T, cfgNames ...string) *clockmgr.ClockManager {
	t.Helper()
	cm := clockmgr.Init("test-node", make(chan event.Event), nil, nil, nil, nil)
	for _, n := range cfgNames {
		_, err := cm.AddClock(n, event.OC, nil, "")
		assert.NoError(t, err)
	}
	return cm
}

func TestStartOne_NilProfileDoesNotPanic(t *testing.T) {
	called := false
	pm := &ProcessManager{
		daemon: &Daemon{
			pluginManager: plugin.PluginManager{
				Plugins: map[string]*plugin.Plugin{
					testE810: {
						AfterRunPTPCommand: func(_ *interface{}, nodeProfile *ptpv1.PtpProfile, _ string) error {
							called = true
							_ = nodeProfile.Plugins
							return nil
						},
					},
				},
			},
		},
	}
	p := &stubProcess{name: pmcSocketName, state: process.Created}
	assert.NotPanics(t, func() {
		pm.startOne(context.Background(), p)
	})
	assert.False(t, called)
	assert.Equal(t, 1, p.starts)
}

func TestStartOne_CallsAfterRunPTPCommandWithProfile(t *testing.T) {
	var gotCmd string
	prof := &ptpv1.PtpProfile{}
	pm := &ProcessManager{
		daemon: &Daemon{
			pluginManager: plugin.PluginManager{
				Plugins: map[string]*plugin.Plugin{
					testE810: {
						AfterRunPTPCommand: func(_ *interface{}, nodeProfile *ptpv1.PtpProfile, command string) error {
							gotCmd = command
							assert.Equal(t, prof, nodeProfile)
							return nil
						},
					},
				},
			},
		},
	}
	p := &stubProcess{name: pmcSocketName, state: process.Created, profile: prof}
	pm.startOne(context.Background(), p)
	assert.Equal(t, pmcSocketName, gotCmd)
	assert.Equal(t, 1, p.starts)
}

func TestForwardEvents_HopsEvent(t *testing.T) {
	inbound := make(chan event.Event, 1)
	handler := make(chan event.Event, 1)
	pm := &ProcessManager{
		eventChannel:   inbound,
		handlerChannel: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.forwardEvents(ctx)

	want := event.Event{
		Source:    event.PTP4l,
		CfgName:   ptp4lConfig,
		ClockType: event.OC,
		Reset:     true,
	}
	inbound <- want

	select {
	case got := <-handler:
		assert.Equal(t, want, got)
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive hopped event on handlerChannel")
	}
}

func TestForwardEvents_CancelReturns(t *testing.T) {
	inbound := make(chan event.Event)
	handler := make(chan event.Event)
	pm := &ProcessManager{
		eventChannel:   inbound,
		handlerChannel: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		pm.forwardEvents(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("forwardEvents did not return after cancel")
	}
}

func TestForwardEvents_ProcessStatusDownDeadRestarts(t *testing.T) {
	inbound := make(chan event.Event, 1)
	handler := make(chan event.Event, 1)
	stub := &stubProcess{name: string(event.PTP4l), state: process.Dead}
	pm := &ProcessManager{
		process:        []process.Process{stub},
		eventChannel:   inbound,
		handlerChannel: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.forwardEvents(ctx)

	inbound <- event.ProcessStatusEvent(event.PTP4l, "ptp4l.0.config", event.OC, "", PtpProcessDown)

	assert.Eventually(t, func() bool { return stub.starts == 1 }, 2*time.Second, 10*time.Millisecond)
	assert.Empty(t, handler, "ProcessStatusData must not be forwarded to handler")
}

func TestForwardEvents_ProcessStatusDownStoppedNoRestart(t *testing.T) {
	inbound := make(chan event.Event, 1)
	handler := make(chan event.Event, 1)
	stub := &stubProcess{name: string(event.PTP4l), state: process.Stopped}
	pm := &ProcessManager{
		process:        []process.Process{stub},
		eventChannel:   inbound,
		handlerChannel: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.forwardEvents(ctx)

	inbound <- event.ProcessStatusEvent(event.PTP4l, "ptp4l.0.config", event.OC, "", PtpProcessDown)

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, stub.starts)
	assert.Empty(t, handler, "ProcessStatusData must not be forwarded to handler")
}

func TestForwardEvents_ProcessStatusUpNoRestart(t *testing.T) {
	inbound := make(chan event.Event, 1)
	handler := make(chan event.Event, 1)
	stub := &stubProcess{name: string(event.PTP4l), state: process.Dead}
	pm := &ProcessManager{
		process:        []process.Process{stub},
		eventChannel:   inbound,
		handlerChannel: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.forwardEvents(ctx)

	inbound <- event.ProcessStatusEvent(event.PTP4l, "ptp4l.0.config", event.OC, "", PtpProcessUp)

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, stub.starts)
	assert.Empty(t, handler, "ProcessStatusData must not be forwarded to handler")
}

func TestForwardEvents_ProcessStatusDownRestartsDep(t *testing.T) {
	inbound := make(chan event.Event, 1)
	handler := make(chan event.Event, 1)
	dep := &stubProcess{name: string(event.GPSD), state: process.Dead}
	parent := &stubProcess{name: string(event.TS2PHC), state: process.Running, deps: []process.Process{dep}}
	pm := &ProcessManager{
		process:        []process.Process{parent},
		eventChannel:   inbound,
		handlerChannel: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.forwardEvents(ctx)

	inbound <- event.ProcessStatusEvent(event.GPSD, "ts2phc.0.config", event.GM, "", PtpProcessDown)

	assert.Eventually(t, func() bool { return dep.starts == 1 }, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, 0, parent.starts)
	assert.Empty(t, handler, "ProcessStatusData must not be forwarded to handler")
}

func TestStartProcesses_MissingConditionStartsImmediately(t *testing.T) {
	dep := &stubProcess{name: "gpspipe"}
	parent := &stubProcess{name: "ts2phc", deps: []process.Process{dep}}
	pm := &ProcessManager{
		process:        []process.Process{parent},
		eventChannel:   make(chan event.Event),
		handlerChannel: make(chan event.Event, 1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		pm.StartProcesses(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("StartProcesses blocked; 3s init delays should be gone")
	}
	assert.Equal(t, 1, dep.starts)
	assert.Equal(t, 1, parent.starts)
}

func TestStartProcesses_AlreadyRunningNotStarted(t *testing.T) {
	parent := &stubProcess{
		name:  "ts2phc",
		state: process.Running,
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnProcessUp{Source: event.GPSD, ConfigName: ts2phcConf},
		},
	}
	pm := &ProcessManager{
		process:        []process.Process{parent},
		eventChannel:   make(chan event.Event),
		handlerChannel: make(chan event.Event, 1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pm.StartProcesses(ctx)
	assert.Equal(t, 0, parent.starts)
}

func TestStartProcesses_OnProcessUpDoesNotStart(t *testing.T) {
	pmc := &stubProcess{
		name: "pmc",
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnProcessUp{Source: event.PTP4l, ConfigName: ptp4lConfig},
		},
	}
	parent := &stubProcess{name: string(event.PTP4l), deps: []process.Process{pmc}}
	pm := &ProcessManager{
		process:        []process.Process{parent},
		eventChannel:   make(chan event.Event),
		handlerChannel: make(chan event.Event, 1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pm.StartProcesses(ctx)
	assert.Equal(t, 0, pmc.starts)
	assert.Equal(t, 1, parent.starts)
}

func TestForwardEvents_OnProcessUpStartsCreated(t *testing.T) {
	inbound := make(chan event.Event, 1)
	handler := make(chan event.Event, 1)
	pmc := &stubProcess{
		name: "pmc",
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnProcessUp{Source: event.PTP4l, ConfigName: ptp4lConfig},
		},
	}
	parent := &stubProcess{name: string(event.PTP4l), state: process.Running, deps: []process.Process{pmc}}
	pm := &ProcessManager{
		process:        []process.Process{parent},
		eventChannel:   inbound,
		handlerChannel: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.forwardEvents(ctx)

	inbound <- event.ProcessStatusEvent(event.PTP4l, "ptp4l.0.config", event.OC, "", PtpProcessUp)

	assert.Eventually(t, func() bool { return pmc.starts == 1 }, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, 0, parent.starts)
}

func TestForwardEvents_OnProcessUpWrongSourceNoStart(t *testing.T) {
	inbound := make(chan event.Event, 2)
	handler := make(chan event.Event, 2)
	pmc := &stubProcess{
		name: "pmc",
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnProcessUp{Source: event.PTP4l, ConfigName: ptp4lConfig},
		},
	}
	parent := &stubProcess{name: string(event.PTP4l), state: process.Running, deps: []process.Process{pmc}}
	pm := &ProcessManager{
		process:        []process.Process{parent},
		eventChannel:   inbound,
		handlerChannel: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.forwardEvents(ctx)

	inbound <- event.ProcessStatusEvent(event.GPSD, "ptp4l.0.config", event.OC, "", PtpProcessUp)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, pmc.starts)

	inbound <- event.ProcessStatusEvent(event.PTP4l, "ptp4l.0.config", event.OC, "", PtpProcessDown)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, pmc.starts)
}

func TestStartProcesses_ImmediateNotRestartedOnHop(t *testing.T) {
	inbound := make(chan event.Event, 1)
	handler := make(chan event.Event, 1)
	parent := &stubProcess{name: string(event.PTP4l)}
	pm := &ProcessManager{
		process:        []process.Process{parent},
		eventChannel:   inbound,
		handlerChannel: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pm.StartProcesses(ctx)
	assert.Equal(t, 1, parent.starts)

	inbound <- event.ProcessStatusEvent(event.PTP4l, "ptp4l.0.config", event.OC, "", PtpProcessUp)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, parent.starts)
	assert.Empty(t, handler, "ProcessStatusData must not be forwarded to handler")
}

// stubEnabler is a process that also satisfies process.Enabler.
type stubEnabler struct {
	stubProcess
	enabled  bool
	enables  int
	disables int
}

func (s *stubEnabler) Enable() error {
	s.enables++
	s.enabled = true
	return nil
}

func (s *stubEnabler) Disable() error {
	s.disables++
	s.enabled = false
	return nil
}

func (s *stubEnabler) IsEnabled() bool { return s.enabled }

func TestEvalActions_StopRunningProcess(t *testing.T) {
	inbound := make(chan event.Event, 1)
	handler := make(chan event.Event, 1)
	phc2sys := &stubProcess{
		name:  phc2sysProcessName,
		state: process.Running,
		conds: map[process.Action]process.Condition{
			process.ActionStop: process.OnPluginEvent{EventName: testGNSSFailover},
		},
	}
	pm := &ProcessManager{
		process:        []process.Process{phc2sys},
		eventChannel:   inbound,
		handlerChannel: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.forwardEvents(ctx)

	inbound <- event.PluginEvent("ntpfailover", testGNSSFailover)

	select {
	case <-handler:
	case <-time.After(2 * time.Second):
		t.Fatal("event was not forwarded")
	}
	// phc2sys.Stop() was called, which sets state to Running in our stub
	// (stub doesn't actually change state, but the method was called)
}

func TestEvalActions_StartStoppedProcess(t *testing.T) {
	inbound := make(chan event.Event, 1)
	handler := make(chan event.Event, 1)
	phc2sys := &stubProcess{
		name:  phc2sysProcessName,
		state: process.Stopped,
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnPluginEvent{EventName: testGNSSRecovered},
		},
	}
	pm := &ProcessManager{
		process:        []process.Process{phc2sys},
		eventChannel:   inbound,
		handlerChannel: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.forwardEvents(ctx)

	inbound <- event.PluginEvent("ntpfailover", testGNSSRecovered)

	assert.Eventually(t, func() bool { return phc2sys.starts == 1 }, 2*time.Second, 10*time.Millisecond)
}

func TestEvalActions_EnableDisableOnEnabler(t *testing.T) {
	inbound := make(chan event.Event, 2)
	handler := make(chan event.Event, 2)
	chronyd := &stubEnabler{
		stubProcess: stubProcess{
			name:  "chronyd",
			state: process.Running,
			conds: map[process.Action]process.Condition{
				process.ActionEnable:  process.OnPluginEvent{EventName: testGNSSFailover},
				process.ActionDisable: process.OnPluginEvent{EventName: testGNSSRecovered},
			},
		},
	}
	pm := &ProcessManager{
		process:        []process.Process{chronyd},
		eventChannel:   inbound,
		handlerChannel: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.forwardEvents(ctx)

	inbound <- event.PluginEvent("ntpfailover", testGNSSFailover)
	select {
	case <-handler:
	case <-time.After(2 * time.Second):
		t.Fatal("event was not forwarded")
	}
	assert.Equal(t, 1, chronyd.enables)
	assert.Equal(t, 0, chronyd.disables)
	assert.True(t, chronyd.IsEnabled())

	inbound <- event.PluginEvent("ntpfailover", testGNSSRecovered)
	select {
	case <-handler:
	case <-time.After(2 * time.Second):
		t.Fatal("event was not forwarded")
	}
	assert.Equal(t, 1, chronyd.enables)
	assert.Equal(t, 1, chronyd.disables)
	assert.False(t, chronyd.IsEnabled())
}

func TestEvalActions_StartNotCheckedWhenRunning(t *testing.T) {
	inbound := make(chan event.Event, 1)
	handler := make(chan event.Event, 1)
	phc2sys := &stubProcess{
		name:  phc2sysProcessName,
		state: process.Running,
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnPluginEvent{EventName: testGNSSRecovered},
		},
	}
	pm := &ProcessManager{
		process:        []process.Process{phc2sys},
		eventChannel:   inbound,
		handlerChannel: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.forwardEvents(ctx)

	inbound <- event.PluginEvent("ntpfailover", testGNSSRecovered)
	select {
	case <-handler:
	case <-time.After(2 * time.Second):
		t.Fatal("event was not forwarded")
	}
	assert.Equal(t, 0, phc2sys.starts)
}

func TestEvalActions_EnableOnlyWhenDisabled(t *testing.T) {
	inbound := make(chan event.Event, 1)
	handler := make(chan event.Event, 1)
	chronyd := &stubEnabler{
		stubProcess: stubProcess{
			name:  "chronyd",
			state: process.Running,
			conds: map[process.Action]process.Condition{
				process.ActionEnable:  process.OnPluginEvent{EventName: testGNSSFailover},
				process.ActionDisable: process.OnPluginEvent{EventName: testGNSSRecovered},
			},
		},
		enabled: true,
	}
	pm := &ProcessManager{
		process:        []process.Process{chronyd},
		eventChannel:   inbound,
		handlerChannel: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.forwardEvents(ctx)

	inbound <- event.PluginEvent("ntpfailover", testGNSSFailover)
	select {
	case <-handler:
	case <-time.After(2 * time.Second):
		t.Fatal("event was not forwarded")
	}
	assert.Equal(t, 0, chronyd.enables)
	assert.Equal(t, 0, chronyd.disables)
}

func TestEvalActions_NoActionWithoutCondition(t *testing.T) {
	inbound := make(chan event.Event, 1)
	handler := make(chan event.Event, 1)
	// Process with no conditions -- ActionStop defaults to Never
	stub := &stubProcess{name: ptp4lProcessName, state: process.Running}
	pm := &ProcessManager{
		process:        []process.Process{stub},
		eventChannel:   inbound,
		handlerChannel: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.forwardEvents(ctx)

	inbound <- event.PluginEvent("ntpfailover", testGNSSFailover)
	select {
	case <-handler:
	case <-time.After(2 * time.Second):
		t.Fatal("event was not forwarded")
	}
	// Running process should NOT be stopped because ActionStop defaults to Never
	assert.Equal(t, process.Running, stub.state)
}

func TestEvalActions_FullFailoverFlow(t *testing.T) {
	inbound := make(chan event.Event, 4)
	handler := make(chan event.Event, 4)
	phc2sys := &stubProcess{
		name:  phc2sysProcessName,
		state: process.Running,
		conds: map[process.Action]process.Condition{
			process.ActionStop:  process.OnPluginEvent{EventName: testGNSSFailover},
			process.ActionStart: process.OnPluginEvent{EventName: testGNSSRecovered},
		},
	}
	chronyd := &stubEnabler{
		stubProcess: stubProcess{
			name:  "chronyd",
			state: process.Running,
			conds: map[process.Action]process.Condition{
				process.ActionEnable:  process.OnPluginEvent{EventName: testGNSSFailover},
				process.ActionDisable: process.OnPluginEvent{EventName: testGNSSRecovered},
			},
		},
	}
	pm := &ProcessManager{
		process:        []process.Process{phc2sys, chronyd},
		eventChannel:   inbound,
		handlerChannel: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.forwardEvents(ctx)

	// Failover: stop phc2sys + enable chronyd
	inbound <- event.PluginEvent("ntpfailover", testGNSSFailover)
	select {
	case <-handler:
	case <-time.After(2 * time.Second):
		t.Fatal("failover event was not forwarded")
	}
	assert.Equal(t, 1, chronyd.enables, "chronyd should be enabled on failover")

	// Simulate phc2sys being stopped (stub Stop doesn't change state, set manually)
	phc2sys.state = process.Stopped

	// Recovery: start phc2sys + disable chronyd
	inbound <- event.PluginEvent("ntpfailover", testGNSSRecovered)
	assert.Eventually(t, func() bool { return phc2sys.starts == 1 }, 2*time.Second, 10*time.Millisecond)
	select {
	case <-handler:
	case <-time.After(2 * time.Second):
		t.Fatal("recovery event was not forwarded")
	}
	assert.Equal(t, 1, chronyd.disables, "chronyd should be disabled on recovery")
}

func TestForwardEvents_AllRequiresSameEvent(t *testing.T) {
	inbound := make(chan event.Event, 2)
	handler := make(chan event.Event, 2)
	parent := &stubProcess{
		name: string(event.TS2PHC),
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.All{Conditions: []process.Condition{
				process.OnProcessUp{Source: event.GPSD},
				process.OnProcessUp{Source: event.GPSPIPE},
			}},
		},
	}
	pm := &ProcessManager{
		process:        []process.Process{parent},
		eventChannel:   inbound,
		handlerChannel: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.forwardEvents(ctx)

	inbound <- event.ProcessStatusEvent(event.GPSD, "ts2phc.0.config", event.GM, "", PtpProcessUp)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, parent.starts)

	inbound <- event.ProcessStatusEvent(event.GPSPIPE, "ts2phc.0.config", event.GM, "", PtpProcessUp)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, parent.starts, "stateless All requires every nested Met on the same event")
}

func TestEvalActions_DelayedPhc2sysStartOnSubSecondOffset(t *testing.T) {
	const cfgName = "ts2phc.0.config"
	const count = 3
	inbound := make(chan event.Event, 10)
	handler := make(chan event.Event, 10)

	cm := testClockMgr(t, cfgName)

	phc2sys := &stubProcess{
		name:  phc2sysProcessName,
		state: process.Created,
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnStateAndOffsetForCount{
				ConfigName: cfgName,
				Source:     string(event.TS2PHC),
				State:      event.PTP_LOCKED,
				MaxOffset:  1e9,
				Count:      count,
			},
		},
	}
	pm := &ProcessManager{
		process:        []process.Process{phc2sys},
		eventChannel:   inbound,
		handlerChannel: handler,
		clockMgr:       cm,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.forwardEvents(ctx)

	mkEvent := func(offset int64, state event.PTPState) event.Event {
		return event.Event{
			Source:  event.TS2PHC,
			CfgName: cfgName,
			IFace:   testETH0,
			Data: &event.OffsetData{
				State:  state,
				Offset: offset,
			},
		}
	}
	drain := func() {
		select {
		case <-handler:
		case <-time.After(2 * time.Second):
			t.Fatal("event was not forwarded")
		}
	}
	send := func(offset int64, state event.PTPState) {
		ev := mkEvent(offset, state)
		cm.GetClock(cfgName).GetData(event.TS2PHC).AddEvent(ev)
		inbound <- ev
		drain()
	}

	send(37000000000, event.PTP_LOCKED)
	assert.Equal(t, 0, phc2sys.starts, "large offset should not start phc2sys")

	send(1000000000, event.PTP_LOCKED)
	assert.Equal(t, 0, phc2sys.starts, "boundary offset should not start phc2sys")

	send(-2000000000, event.PTP_LOCKED)
	assert.Equal(t, 0, phc2sys.starts, "negative super-second offset should not start phc2sys")

	for i := 0; i < count; i++ {
		send(int64(100000+i), event.PTP_LOCKED)
	}
	assert.Equal(t, 0, phc2sys.starts, "should not start yet, need > count samples")

	send(-500000000, event.PTP_LOCKED)
	assert.Eventually(t, func() bool { return phc2sys.starts == 1 }, 2*time.Second, 10*time.Millisecond)
}

func TestEvalActions_DelayedPhc2sysWrongStateDoesNotStart(t *testing.T) {
	const cfgName = "ts2phc.0.config"
	inbound := make(chan event.Event, 10)
	handler := make(chan event.Event, 10)

	cm := testClockMgr(t, cfgName)

	phc2sys := &stubProcess{
		name:  phc2sysProcessName,
		state: process.Created,
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnStateAndOffsetForCount{
				ConfigName: cfgName,
				Source:     string(event.TS2PHC),
				State:      event.PTP_LOCKED,
				MaxOffset:  1e9,
				Count:      1,
			},
		},
	}
	pm := &ProcessManager{
		process:        []process.Process{phc2sys},
		eventChannel:   inbound,
		handlerChannel: handler,
		clockMgr:       cm,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.forwardEvents(ctx)

	// Sub-second offset but FREERUN state should NOT start phc2sys
	for i := 0; i < 5; i++ {
		ev := event.Event{
			Source:  event.TS2PHC,
			CfgName: cfgName,
			IFace:   testETH0,
			Data: &event.OffsetData{
				State:  event.PTP_FREERUN,
				Offset: 50000,
			},
		}
		cm.GetClock(cfgName).GetData(event.TS2PHC).AddEvent(ev)
		inbound <- ev
		select {
		case <-handler:
		case <-time.After(2 * time.Second):
			t.Fatal("event was not forwarded")
		}
	}
	assert.Equal(t, 0, phc2sys.starts, "FREERUN state should not start phc2sys regardless of offset")
}

func TestEvalActions_DelayedPhc2sysTBCWaitsForPtp4l(t *testing.T) {
	const cfgName = "ptp4l.0.config"
	inbound := make(chan event.Event, 10)
	handler := make(chan event.Event, 10)
	cm := testClockMgr(t, cfgName)

	phc2sys := &stubProcess{
		name:  phc2sysProcessName,
		state: process.Created,
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnStateAndOffsetForCount{
				ConfigName: cfgName,
				Source:     string(event.PTP4l),
				State:      event.PTP_LOCKED,
				MaxOffset:  1e9,
				Count:      1,
			},
		},
	}
	pm := &ProcessManager{
		process:        []process.Process{phc2sys},
		eventChannel:   inbound,
		handlerChannel: handler,
		clockMgr:       cm,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.forwardEvents(ctx)

	drain := func() {
		select {
		case <-handler:
		case <-time.After(2 * time.Second):
			t.Fatal("event was not forwarded")
		}
	}

	ts2phcEv := event.Event{
		Source:  event.TS2PHC,
		CfgName: "ts2phc.0.config",
		IFace:   testETH0,
		Data:    &event.OffsetData{State: event.PTP_LOCKED, Offset: 50000},
	}
	cm.GetClock("ts2phc.0.config").GetData(event.TS2PHC).AddEvent(ts2phcEv)
	cm.GetClock("ts2phc.0.config").GetData(event.TS2PHC).AddEvent(ts2phcEv)
	cm.GetClock("ts2phc.0.config").GetData(event.TS2PHC).AddEvent(ts2phcEv)
	inbound <- ts2phcEv
	drain()
	assert.Equal(t, 0, phc2sys.starts, "T-BC phc2sys must not start on ts2phc events")

	ptp4lEv := event.Event{
		Source:  event.PTP4l,
		CfgName: cfgName,
		IFace:   testETH0,
		Data:    &event.OffsetData{State: event.PTP_LOCKED, Offset: 50000},
	}
	cm.GetClock(cfgName).GetData(event.PTP4l).AddEvent(ptp4lEv)
	cm.GetClock(cfgName).GetData(event.PTP4l).AddEvent(ptp4lEv)
	cm.GetClock(cfgName).GetData(event.PTP4l).AddEvent(ptp4lEv)
	inbound <- ptp4lEv
	assert.Eventually(t, func() bool { return phc2sys.starts == 1 }, 2*time.Second, 10*time.Millisecond)
}

func TestEvalActions_DelayedPhc2sysHAProfile(t *testing.T) {
	master1 := ptp4lConfig
	master2 := testPtp4l1Config
	inbound := make(chan event.Event, 10)
	handler := make(chan event.Event, 10)
	cm := testClockMgr(t, master1, master2)

	phc2sys := &stubProcess{
		name:  phc2sysProcessName,
		state: process.Created,
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.Any{Conditions: []process.Condition{
				process.OnStateAndOffsetForCount{
					ConfigName: master1, Source: string(event.PTP4l),
					State: event.PTP_LOCKED, MaxOffset: 1e9, Count: 1,
				},
				process.OnStateAndOffsetForCount{
					ConfigName: master2, Source: string(event.PTP4l),
					State: event.PTP_LOCKED, MaxOffset: 1e9, Count: 1,
				},
			}},
		},
	}
	pm := &ProcessManager{
		process:        []process.Process{phc2sys},
		eventChannel:   inbound,
		handlerChannel: handler,
		clockMgr:       cm,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.forwardEvents(ctx)

	drain := func() {
		select {
		case <-handler:
		case <-time.After(2 * time.Second):
			t.Fatal("event was not forwarded")
		}
	}

	large := event.Event{
		Source:  event.PTP4l,
		CfgName: master1,
		IFace:   "ens1f1",
		Data:    &event.OffsetData{State: event.PTP_LOCKED, Offset: 37000000000},
	}
	cm.GetClock(master1).GetData(event.PTP4l).AddEvent(large)
	cm.GetClock(master1).GetData(event.PTP4l).AddEvent(large)
	cm.GetClock(master1).GetData(event.PTP4l).AddEvent(large)
	inbound <- large
	drain()
	assert.Equal(t, 0, phc2sys.starts, "large offset from HA-linked profile should not start phc2sys")

	small := event.Event{
		Source:  event.PTP4l,
		CfgName: master2,
		IFace:   "ens2f0",
		Data:    &event.OffsetData{State: event.PTP_LOCKED, Offset: 500000000},
	}
	cm.GetClock(master2).GetData(event.PTP4l).AddEvent(small)
	cm.GetClock(master2).GetData(event.PTP4l).AddEvent(small)
	cm.GetClock(master2).GetData(event.PTP4l).AddEvent(small)
	inbound <- small
	assert.Eventually(t, func() bool { return phc2sys.starts == 1 }, 2*time.Second, 10*time.Millisecond)
}

func TestPhc2sysOffsetStartCondition_TGM(t *testing.T) {
	c := phc2sysOffsetStartCondition(ptpProcessEnv{runID: 2, clockType: event.GM, nodeProfile: &ptpv1.PtpProfile{PtpSettings: map[string]string{"clockType": TGM}}})
	assert.Equal(t, process.OnStateAndOffsetForCount{
		ConfigName: "ts2phc.2.config",
		Source:     string(event.TS2PHC),
		State:      event.PTP_LOCKED,
		MaxOffset:  1e9,
		Count:      3,
	}, c)
}

func TestPhc2sysOffsetStartCondition_InferredGM(t *testing.T) {
	c := phc2sysOffsetStartCondition(ptpProcessEnv{runID: 0, clockType: event.GM})
	assert.Equal(t, process.OnStateAndOffsetForCount{
		ConfigName: "ts2phc.0.config",
		Source:     string(event.TS2PHC),
		State:      event.PTP_LOCKED,
		MaxOffset:  1e9,
		Count:      3,
	}, c)
}

func TestPhc2sysOffsetStartCondition_TBC(t *testing.T) {
	c := phc2sysOffsetStartCondition(ptpProcessEnv{runID: 0, clockType: event.TBC, nodeProfile: &ptpv1.PtpProfile{PtpSettings: map[string]string{"clockType": TBC}}})
	assert.Equal(t, process.OnStateAndOffsetForCount{
		ConfigName: ptp4lConfig,
		Source:     string(event.PTP4l),
		State:      event.PTP_LOCKED,
		MaxOffset:  1e9,
		Count:      3,
	}, c)
}

func TestPhc2sysOffsetStartCondition_HA(t *testing.T) {
	master1 := "test-bc-master1"
	master2 := "test-bc-master2"
	dn := &Daemon{processManager: &ProcessManager{
		process: []process.Process{
			&ptpProcess{ExecProcess: ExecProcess{name: ptp4lProcessName, configName: "ptp4l.0.config"}, nodeProfile: &ptpv1.PtpProfile{Name: &master1}},
			&ptpProcess{ExecProcess: ExecProcess{name: ptp4lProcessName, configName: testPtp4l1Config}, nodeProfile: &ptpv1.PtpProfile{Name: &master2}},
		},
	}}
	c := phc2sysOffsetStartCondition(ptpProcessEnv{
		runID:       5,
		nodeProfile: &ptpv1.PtpProfile{PtpSettings: map[string]string{PTP_HA_IDENTIFIER: master1 + "," + master2, "clockType": TBC}},
		dn:          dn,
	})
	anyCond, ok := c.(process.Any)
	if !assert.True(t, ok, "HA should wrap per-config conditions in Any") {
		return
	}
	assert.Len(t, anyCond.Conditions, 2)
	assert.Equal(t, "ptp4l.0.config", anyCond.Conditions[0].(process.OnStateAndOffsetForCount).ConfigName)
	assert.Equal(t, testPtp4l1Config, anyCond.Conditions[1].(process.OnStateAndOffsetForCount).ConfigName)
	assert.Equal(t, string(event.PTP4l), anyCond.Conditions[0].(process.OnStateAndOffsetForCount).Source)
}

func TestProcessStatusEmitConfig_SkipsDelayedStart(t *testing.T) {
	p := &ptpProcess{
		ExecProcess: ExecProcess{
			name:       phc2sysProcessName,
			messageTag: "[phc2sys.0.config]",
			configName: "phc2sys.0.config",
			conditions: map[process.Action]process.Condition{
				process.ActionStart: process.OnStateAndOffsetForCount{
					ConfigName: "ts2phc.0.config",
					Source:     string(event.TS2PHC),
					State:      event.PTP_LOCKED,
					MaxOffset:  1e9,
					Count:      3,
				},
			},
		},
	}
	_, emit := processStatusEmitConfig(p)
	assert.False(t, emit)
}

func TestProcessStatusEmitConfig_SkipsHADelayedStart(t *testing.T) {
	p := &ptpProcess{
		ExecProcess: ExecProcess{
			name:       phc2sysProcessName,
			messageTag: "[phc2sys.0.config]",
			configName: "phc2sys.0.config",
			conditions: map[process.Action]process.Condition{
				process.ActionStart: process.Any{Conditions: []process.Condition{
					process.OnStateAndOffsetForCount{ConfigName: "ptp4l.0.config", Source: string(event.PTP4l)},
					process.OnStateAndOffsetForCount{ConfigName: testPtp4l1Config, Source: string(event.PTP4l)},
				}},
			},
		},
	}
	assert.NotPanics(t, func() {
		_, emit := processStatusEmitConfig(p)
		assert.False(t, emit)
	})
}

func TestProcessStatusEmitConfig_EmitsRunningProcess(t *testing.T) {
	p := &ptpProcess{
		ExecProcess: ExecProcess{
			name:       ptp4lProcessName,
			messageTag: "[ptp4l.0.config]",
			configName: "ptp4l.0.config",
			state:      process.Running,
		},
	}
	cfg, emit := processStatusEmitConfig(p)
	assert.True(t, emit)
	assert.Equal(t, "ptp4l.0.config", cfg)
	assert.Equal(t, PtpProcessUp, processStatusForEmit(p))
}

func TestProcessStatusEmitConfig_EmitsChronyd(t *testing.T) {
	c := &chronydProcess{ptpProcess: &ptpProcess{
		ExecProcess: ExecProcess{
			name:       chronydProcessName,
			messageTag: "[chronyd.0.config]",
			configName: "chronyd.0.config",
			state:      process.Running,
		},
	}}
	cfg, emit := processStatusEmitConfig(c)
	assert.True(t, emit)
	assert.Equal(t, "chronyd.0.config", cfg)
}
