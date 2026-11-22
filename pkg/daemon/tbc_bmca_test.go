package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clockmgr"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/hardwareconfig"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	parserconstants "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/require"
)

const bmcaTestConfig = "ptp4l.1.config"

func newBMCATestProcess(t *testing.T, hcm *hardwareconfig.HardwareConfigManager) (*ptpProcess, *clockmgr.ClockManager, func() []bool) {
	t.Helper()
	InitializeOffsetMaps()
	t.Cleanup(InitializeOffsetMaps)
	profile := &ptpv1.PtpProfile{Name: utils.ToPtr("t-bc_test-profile"), PtpSettings: map[string]string{
		"clockType": TBC, "leadingInterface": "eno5", "clockId[eno5]": "123456789", "inSyncConditionThreshold": "10",
	}}
	events := make(chan event.Event, 20)
	cm := clockmgr.Init("test", events, nil, nil, nil, nil)
	clk, err := clock.NewTBC(bmcaTestConfig, cm.GetUtcOffset, &pmc.MockClient{}, "ptp4l.0.config")
	require.NoError(t, err)
	clk.SetConfiguredLeadingInterface("eno5")
	require.NoError(t, cm.AddClock(clk))
	p := &ptpProcess{
		ExecProcess: ExecProcess{name: event.Ptp4lProcessName, configName: bmcaTestConfig, messageTag: "[" + bmcaTestConfig + ":{level}]", eventCh: events},
		nodeProfile: profile, clockType: event.TBC, logParser: parser.NewPTP4LExtractor(), ptpClockThreshold: getPTPThreshold(profile),
		ifaces:        config.IFaces{{Name: "eno2"}, {Name: "eno3"}, {Name: "eno20"}},
		tBCAttributes: tBCProcessAttributes{trPortsConfigFile: bmcaTestConfig, trIfaceNames: []string{"eno2", "eno3"}},
		dn:            &Daemon{hardwareConfigManager: hcm, processManager: &ProcessManager{clockMgr: cm}},
		pm: &plugin.PluginManager{Plugins: map[string]*plugin.Plugin{
			"test": {AfterRunPTPCommand: func(_ *interface{}, _ *ptpv1.PtpProfile, _ string) error { return nil }},
		}},
	}
	policy, err := resolveSourceQualificationPolicy(profile)
	require.NoError(t, err)
	require.NoError(t, p.configureSourceQualification(policy))
	// Real aggregate prerequisites are already healthy. Retained LOCKED PTP
	// details must not establish readiness before BMCA/qualification/application.
	for i := 0; i <= event.WindowSize; i++ {
		now := time.Now().UnixMilli()
		cm.ProcessEvent(event.Event{Source: event.DPLL, CfgName: bmcaTestConfig, IFace: "eno5", Time: now, Data: &event.DPLLData{
			State: event.PTP_LOCKED, LeadingSource: true, Offset: utils.ToPtr(int64(5)),
			InSyncConditionThreshold: 100, InSyncConditionTimes: 1, ToFreeRunThreshold: 1500, MaxInSpecOffset: 500,
		}})
		cm.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: bmcaTestConfig, IFace: "eno2", Time: now,
			Data: &event.OffsetData{State: event.PTP_LOCKED, Offset: 5}})
	}
	var mu sync.Mutex
	var actions []bool
	apply := p.sourceQualification.apply
	p.sourceQualification.apply = func(ready bool) error {
		mu.Lock()
		actions = append(actions, ready)
		mu.Unlock()
		return apply(ready)
	}
	p.sourceQualification.ctx = startQualificationClock(t, cm)
	t.Cleanup(p.sourceQualification.close)
	return p, cm, func() []bool { mu.Lock(); defer mu.Unlock(); return append([]bool(nil), actions...) }
}

func bmcaLine(message string) string {
	return fmt.Sprintf("ptp4l[1.123]: [%s:5] %s", bmcaTestConfig, message)
}

// A rejected no-op observation is a barrier behind queued action acknowledgements.
// Read only the resulting detached snapshot, never live clock state off-loop.
func bmcaSnapshot(t *testing.T, p *ptpProcess, cm *clockmgr.ClockManager) process.EventStats {
	t.Helper()
	p.observeSource("", &event.PTPSourceObservation{})
	select {
	case stats := <-cm.StatsUpdatesCh():
		return stats
	case <-time.After(time.Second):
		t.Fatal("clock snapshot was not published")
		return process.EventStats{}
	}
}

func exerciseTBCBMCA(t *testing.T, hcm *hardwareconfig.HardwareConfigManager, assertInput func(bool)) {
	t.Helper()
	p, cm, actions := newBMCATestProcess(t, hcm)
	line := func(message string) { p.processOutput(bmcaLine(message)) }
	assertReady := func(want bool) {
		t.Helper()
		stats := bmcaSnapshot(t, p, cm)
		require.Equal(t, want, stats.SourceQualified[bmcaTestConfig])
		if assertInput != nil {
			assertInput(want)
		}
	}
	line("port 1 (eno2): UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED")
	for i := 0; i < 100; i++ {
		line("unrelated diagnostic")
	}
	require.Empty(t, actions(), "port changes and cached zero offsets must never apply input actions")
	line("selected best master clock upstream-A")
	assertReady(false)
	for i := 0; i < 4; i++ {
		line("master offset 5 s2 freq 0 path delay 100")
	}
	line("rms 5 max 5 freq 0 +/- 0 delay 100 +/- 0")
	assertReady(false)
	line("master offset -5 s2 freq 0 path delay 100")
	assertReady(true)
	require.Equal(t, event.PTP_LOCKED, bmcaSnapshot(t, p, cm).ClockStates[bmcaTestConfig])
	before, _ := cm.SourceQualification(bmcaTestConfig)

	// Both named and numbered role transitions are metadata only, including a
	// non-TR port whose name collides with a configured port's prefix.
	line("port 1 (eno2): SLAVE to FAULTY on FAULT_DETECTED (FT_UNSPECIFIED)")
	line("port 2: MASTER to UNCALIBRATED on RS_SLAVE")
	line("selected best master clock upstream-B")
	line("port 2: UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED")
	line("port 3 (eno20): UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED")
	line("port 2 (eno3): SLAVE to MASTER on ANNOUNCE_RECEIPT_TIMEOUT_EXPIRES")
	assertReady(true)
	after, _ := cm.SourceQualification(bmcaTestConfig)
	require.Equal(t, event.PTP_LOCKED, bmcaSnapshot(t, p, cm).ClockStates[bmcaTestConfig], "role changes cannot force aggregate holdover")
	require.Equal(t, "eno3", after.IFace)
	require.Equal(t, before.Source, after.Source, "port selection cannot change the qualified timing stream identity")
	require.Equal(t, []bool{false, true}, actions(), "hitless reselection must not run holdover entry/exit")

	p.processOutput("ptp4l[2]: [ptp4l.0.config] selected local clock downstream-clock as best master")
	assertReady(true)
	line("selected local clock arbitrary-local-id as best master")
	assertReady(false)
	require.Equal(t, event.PTP_HOLDOVER, bmcaSnapshot(t, p, cm).ClockStates[bmcaTestConfig], "stale LOCKED details cannot hide loss")
	line("port 1: UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED")
	for i := 0; i < 8; i++ {
		line("master offset 5 s2 freq 0 path delay 100")
	}
	assertReady(false)
	line("selected best master clock upstream-A")
	assertReady(false)
	for i := 0; i < 4; i++ {
		line("master offset 5 s2 freq 0 path delay 100")
	}
	assertReady(false)
	line("master offset 5 s2 freq 0 path delay 100")
	assertReady(true)
	require.Equal(t, []bool{false, true, false, true}, actions(), "recovery must re-enable input through the same apply cache")

	for i := 0; i < 5; i++ {
		line("master offset 11 s2 freq 0 path delay 100")
	}
	assertReady(false)
	for i := 0; i < 5; i++ {
		line("master offset 5 s2 freq 0 path delay 100")
	}
	assertReady(true)
	p.invalidateSourceQualification()
	assertReady(false)
}

func TestTBCBMCAPluginPath(t *testing.T) {
	exerciseTBCBMCA(t, nil, nil)
}

func TestTBCBMCAInputFailureRetriesWithoutAnotherLog(t *testing.T) {
	for _, failingReady := range []bool{false, true} {
		t.Run(fmt.Sprintf("ready=%t", failingReady), func(t *testing.T) {
			p, cm, _ := newBMCATestProcess(t, nil)
			line := func(message string) { p.processOutput(bmcaLine(message)) }
			line("selected best master clock upstream-A")
			for i := 0; i < 5; i++ {
				line("master offset 5 s2 freq 0 path delay 100")
			}
			require.Equal(t, event.PTP_LOCKED, bmcaSnapshot(t, p, cm).ClockStates[bmcaTestConfig])
			var fail atomic.Bool
			fail.Store(true)
			p.sourceQualification.mu.Lock()
			apply := p.sourceQualification.apply
			p.sourceQualification.apply = func(ready bool) error {
				if ready == failingReady && fail.Load() {
					return errors.New("pin action failed")
				}
				return apply(ready)
			}
			p.sourceQualification.mu.Unlock()
			line("selected local clock local-id as best master")
			require.Equal(t, event.PTP_HOLDOVER, bmcaSnapshot(t, p, cm).ClockStates[bmcaTestConfig], "loss is observed even if disabling fails")
			if failingReady {
				line("selected best master clock upstream-A")
				for i := 0; i < 5; i++ {
					line("master offset 5 s2 freq 0 path delay 100")
				}
				stats := bmcaSnapshot(t, p, cm)
				require.True(t, stats.SourceQualified[bmcaTestConfig], "failed hardware does not hide qualified source evidence")
				require.Equal(t, event.PTP_HOLDOVER, stats.ClockStates[bmcaTestConfig], "failed enabling cannot report successful recovery")
			}
			fail.Store(false)
			require.Eventually(t, func() bool {
				q := p.sourceQualification
				q.mu.Lock()
				defer q.mu.Unlock()
				return q.appliedKnown && q.applied == failingReady
			}, 2*time.Second, time.Millisecond, "failed input action must retry without another log line")
			if failingReady {
				require.Equal(t, event.PTP_LOCKED, bmcaSnapshot(t, p, cm).ClockStates[bmcaTestConfig])
			}
		})
	}
}

func TestTBCBMCARetryCannotReenableLostSource(t *testing.T) {
	p, cm, _ := newBMCATestProcess(t, nil)
	var enabled atomic.Bool
	p.sourceQualification.mu.Lock()
	p.sourceQualification.apply = func(ready bool) error {
		if ready {
			return errors.New("enable failed")
		}
		enabled.Store(false)
		return nil
	}
	p.sourceQualification.mu.Unlock()
	p.observeSource("", &event.PTPSourceObservation{Selection: parserconstants.BMCAExternal})
	for i := 0; i < 5; i++ {
		p.observeSource("", &event.PTPSourceObservation{ServoState: "s2", Offset: 5})
	}
	p.observeSource("", &event.PTPSourceObservation{Selection: parserconstants.BMCALocal})
	require.False(t, bmcaSnapshot(t, p, cm).SourceQualified[bmcaTestConfig])
	p.sourceQualification.mu.Lock()
	require.Nil(t, p.sourceQualification.retry, "successful loss action cancels obsolete enable retries")
	p.sourceQualification.mu.Unlock()
	require.False(t, enabled.Load())
}

func TestTBCInputAcknowledgementWaitsForDeliveryOrShutdown(t *testing.T) {
	for _, outcome := range []string{"delivery", "cancel", "close"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			p := &ptpProcess{ExecProcess: ExecProcess{eventCh: make(chan event.Event, 1)},
				sourceQualification: &sourceQualificationRuntime{done: make(chan struct{})}}
			p.eventCh <- event.Event{CfgName: "pending"}
			done := make(chan struct{})
			go func() {
				defer close(done)
				p.sendSourceInputApplied(ctx, &event.PTPSourceApplied{Generation: 7, Revision: 3, Enabled: true})
			}()
			t.Cleanup(func() { cancel(); <-done })
			select {
			case <-done:
				t.Fatal("state-bearing acknowledgement was dropped")
			case <-time.After(20 * time.Millisecond):
			}
			require.True(t, p.sourceQualification.mu.TryLock(), "delivery must not hold the action mutex")
			p.sourceQualification.mu.Unlock()
			switch outcome {
			case "delivery":
				require.Equal(t, "pending", (<-p.eventCh).CfgName)
				require.Equal(t, &event.PTPSourceApplied{Generation: 7, Revision: 3, Enabled: true}, (<-p.eventCh).Data)
			case "cancel":
				cancel()
			case "close":
				p.sourceQualification.close()
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("acknowledgement sender did not stop")
			}
		})
	}
}
