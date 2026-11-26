package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
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
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stats, err := waitForBMCASnapshot(ctx, p, cm)
	require.NoError(t, err)
	return stats
}

func waitForBMCASnapshot(ctx context.Context, p *ptpProcess, cm *clockmgr.ClockManager) (process.EventStats, error) {
	reply := make(chan event.SourceQualification, 1)
	barrier := event.Event{Source: event.PTP4l, CfgName: p.configName, ClockType: p.clockType,
		Data: &event.PTPSourceObservation{Reply: reply}}
	select {
	case p.eventCh <- barrier:
	case <-ctx.Done():
		return process.EventStats{}, fmt.Errorf("send snapshot barrier: %w", ctx.Err())
	}
	select {
	case <-reply:
	case <-ctx.Done():
		return process.EventStats{}, fmt.Errorf("wait for snapshot barrier reply: %w", ctx.Err())
	}
	select {
	case stats := <-cm.StatsUpdatesCh():
		return stats, nil
	case <-ctx.Done():
		return process.EventStats{}, fmt.Errorf("wait for clock snapshot: %w", ctx.Err())
	}
}

func TestBMCASnapshotDeadline(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message string
	}{
		{"send", "send snapshot barrier"},
		{"reply", "wait for snapshot barrier reply"},
		{"snapshot", "wait for clock snapshot"},
		{"shared deadline", "wait for clock snapshot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				capacity := 0
				if tc.name == "send" {
					capacity = 1
				}
				events := make(chan event.Event, capacity)
				if capacity != 0 {
					events <- event.Event{CfgName: "earlier", Data: &event.PTPSourceApplied{Enabled: true}}
				}
				cm := clockmgr.Init("test", events, nil, nil, nil, nil)
				if tc.name == "snapshot" || tc.name == "shared deadline" {
					<-cm.StatsUpdatesCh()
				}
				p := &ptpProcess{ExecProcess: ExecProcess{configName: bmcaTestConfig, eventCh: events}}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				start := time.Now()
				done := make(chan error, 1)
				go func() { _, err := waitForBMCASnapshot(ctx, p, cm); done <- err }()
				if tc.name != "send" {
					if tc.name == "shared deadline" {
						time.Sleep(400 * time.Millisecond)
					}
					var barrier event.Event
					select {
					case barrier = <-events:
					case err := <-done:
						t.Fatalf("snapshot did not send a barrier: %v", err)
					}
					require.Equal(t, event.PTP4l, barrier.Source)
					require.Equal(t, bmcaTestConfig, barrier.CfgName)
					observation, ok := barrier.Data.(*event.PTPSourceObservation)
					require.True(t, ok)
					if tc.name != "reply" {
						if tc.name == "shared deadline" {
							time.Sleep(400 * time.Millisecond)
						}
						observation.Reply <- event.SourceQualification{}
					}
				}
				err := <-done
				require.ErrorIs(t, err, context.DeadlineExceeded)
				require.ErrorContains(t, err, tc.message)
				require.Equal(t, time.Second, time.Since(start))
			})
		})
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
	// Initial BMCA selection starts the source stream, but fresh servo samples
	// are still required before the source qualifies.
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
	assertReady(false)
	line("master offset -5 s2 freq 0 path delay 100")
	assertReady(true)
	require.Equal(t, event.PTP_LOCKED, bmcaSnapshot(t, p, cm).ClockStates[bmcaTestConfig])
	before, _ := cm.SourceQualification(bmcaTestConfig)

	// Hitless external reselection and port-role churn preserve readiness. Both
	// named and numbered roles are metadata, including a non-TR port whose name
	// collides with a configured port's prefix.
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
	// Local BMCA loss immediately revokes readiness despite stale LOCKED details.
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

	// Offset degradation can disqualify an externally selected source; recovery
	// requires another complete run of fresh in-range samples.
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

func TestTBCBMCASelectionDuringApply(t *testing.T) {
	p, cm, actions := newBMCATestProcess(t, nil)
	cm.SetApplying(true)
	bmcaSnapshot(t, p, cm) // Drain the snapshot before the selection arrives.
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.processOutput(bmcaLine("selected best master clock upstream-A"))
	}()
	t.Cleanup(func() { p.sourceQualification.close(); <-done })
	select {
	case stats := <-cm.StatsUpdatesCh():
		require.False(t, stats.SourceQualified[bmcaTestConfig])
	case <-time.After(time.Second):
		t.Fatal("BMCA observation was not received during apply")
	}
	require.Empty(t, actions(), "retaining a selection must not apply hardware during profile apply")
	cm.SetApplying(false)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("apply completion did not release the BMCA observer")
	}
	p.processOutput(bmcaLine("port 1 (eno2): UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED"))
	for i := 0; i < 5; i++ {
		p.processOutput(bmcaLine("master offset 5 s2 freq 0 path delay 100"))
	}
	stats := bmcaSnapshot(t, p, cm)
	require.True(t, stats.SourceQualified[bmcaTestConfig], "the initial selection must survive apply without another BMCA log line")
	require.Equal(t, event.PTP_LOCKED, stats.ClockStates[bmcaTestConfig])
	require.Equal(t, []bool{false, true}, actions(), "replayed evaluation must reach the normal input-action and acknowledgement path")
}

func TestTBCBMCAPendingApplyObservationCanStop(t *testing.T) {
	for _, outcome := range []string{"cancel", "close"} {
		t.Run(outcome, func(t *testing.T) {
			p, cm, actions := newBMCATestProcess(t, nil)
			ctx, cancel := context.WithCancel(p.sourceQualification.ctx)
			defer cancel()
			p.sourceQualification.ctx = ctx
			cm.SetApplying(true)
			bmcaSnapshot(t, p, cm)
			done := make(chan struct{})
			go func() {
				defer close(done)
				p.observeSource("", &event.PTPSourceObservation{Selection: parserconstants.BMCAExternal})
			}()
			t.Cleanup(func() { p.sourceQualification.close(); <-done })
			select {
			case <-cm.StatsUpdatesCh():
			case <-time.After(time.Second):
				t.Fatal("BMCA observation was not received during apply")
			}
			if outcome == "cancel" {
				cancel()
			} else {
				p.sourceQualification.close()
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("stopping the source did not cancel its deferred reply wait")
			}
			cm.RemoveAllClocks()
			cm.SetApplying(false)
			require.Empty(t, actions(), "a stopped observer must not apply a retained result")
		})
	}
}

func TestTBCBMCAInputFailureDoesNotRetry(t *testing.T) {
	for _, failingReady := range []bool{false, true} {
		t.Run(fmt.Sprintf("ready=%t", failingReady), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p, cm, _ := newBMCATestProcess(t, nil)
				line := func(message string) { p.processOutput(bmcaLine(message)) }
				line("selected best master clock upstream-A")
				for i := 0; i < 5; i++ {
					line("master offset 5 s2 freq 0 path delay 100")
				}
				require.Equal(t, event.PTP_LOCKED, bmcaSnapshot(t, p, cm).ClockStates[bmcaTestConfig])
				fail, enabled := true, true
				var actions []bool
				q := p.sourceQualification
				q.mu.Lock()
				apply := q.apply
				q.apply = func(ready bool) error {
					actions = append(actions, ready)
					if ready == failingReady && fail {
						if ready {
							enabled = true // A failed write may partially enable the input.
						}
						return errors.New("pin action failed")
					}
					enabled = ready
					return apply(ready)
				}
				q.mu.Unlock()
				observed := func() ([]bool, bool) {
					q.mu.Lock()
					defer q.mu.Unlock()
					return append([]bool(nil), actions...), enabled
				}
				line("selected local clock local-id as best master")
				require.Equal(t, event.PTP_HOLDOVER, bmcaSnapshot(t, p, cm).ClockStates[bmcaTestConfig], "loss is observed even if disabling fails")
				if failingReady {
					line("selected best master clock upstream-A")
					for i := 0; i < 5; i++ {
						line("master offset 5 s2 freq 0 path delay 100")
					}
				}
				want := []bool{false}
				if failingReady {
					want = append(want, true)
				}
				got, _ := observed()
				require.Equal(t, want, got, "the failing action must be attempted once")
				q.mu.Lock()
				fail = false
				q.mu.Unlock()
				time.Sleep(1500 * time.Millisecond)
				synctest.Wait()
				got, _ = observed()
				require.Equal(t, want, got, "time alone must not repeat a failed action")
				for i := 0; i < 3; i++ {
					p.observeSource("", &event.PTPSourceObservation{ServoState: "s2", Offset: 5})
				}
				selection := parserconstants.BMCALocal
				if failingReady {
					selection = parserconstants.BMCAExternal
				}
				p.observeSource("", &event.PTPSourceObservation{Selection: selection})
				got, _ = observed()
				require.Equal(t, want, got, "unchanged observations must not repeat a failed action")
				stats := bmcaSnapshot(t, p, cm)
				require.Equal(t, failingReady, stats.SourceQualified[bmcaTestConfig])
				require.Equal(t, event.PTP_HOLDOVER, stats.ClockStates[bmcaTestConfig])
				p.tBCAttributes.offsetEventWindow = utils.NewWindow(1)
				p.sendPtp4lOffsetEvent()
				require.Equal(t, event.PTP_FREERUN, bmcaSnapshot(t, p, cm).States[bmcaTestConfig][event.PTP4l], "failed input action must not report a locked offset")
				if failingReady {
					_, enabledNow := observed()
					require.True(t, enabledNow, "model a partially applied enable")
					line("selected local clock local-id as best master")
					got, enabledNow = observed()
					require.Equal(t, []bool{false, true, false}, got, "loss must attempt to disable a partially enabled input")
					require.False(t, enabledNow)
				} else {
					line("selected best master clock upstream-A")
					for i := 0; i < 5; i++ {
						line("master offset 5 s2 freq 0 path delay 100")
					}
					require.Equal(t, event.PTP_LOCKED, bmcaSnapshot(t, p, cm).ClockStates[bmcaTestConfig])
					recoveryActions, enabledNow := observed()
					require.Equal(t, []bool{false, true}, recoveryActions, "recovery must attempt the new action")
					require.True(t, enabledNow)
				}
			})
		})
	}
}

func TestTBCInputAcknowledgementWaitsForDeliveryOrShutdown(t *testing.T) {
	for _, outcome := range []string{"delivery", "cancel", "close"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				actions := make(chan bool, 1)
				p := &ptpProcess{ExecProcess: ExecProcess{eventCh: make(chan event.Event, 1)},
					sourceQualification: &sourceQualificationRuntime{ctx: ctx, generation: 7, done: make(chan struct{}),
						apply: func(ready bool) error { actions <- ready; return nil }}}
				p.eventCh <- event.Event{CfgName: "pending"}
				done := make(chan struct{})
				go func() {
					defer close(done)
					p.applySourceQualification(event.SourceQualification{Generation: 7, Revision: 3, Ready: true})
				}()
				t.Cleanup(func() { cancel(); <-done })
				synctest.Wait()
				select {
				case ready := <-actions:
					require.True(t, ready, "input enable must be applied")
				default:
					t.Fatal("input enable was not applied")
				}
				select {
				case <-done:
					t.Fatal("state-bearing acknowledgement was dropped")
				default:
				}
				require.True(t, p.sourceQualification.mu.TryLock(), "delivery must not hold the action mutex")
				p.sourceQualification.mu.Unlock()
				switch outcome {
				case "delivery":
					require.Equal(t, "pending", (<-p.eventCh).CfgName)
					select {
					case ev := <-p.eventCh:
						require.Equal(t, &event.PTPSourceApplied{Generation: 7, Revision: 3, Enabled: true}, ev.Data)
					case <-time.After(time.Second):
						t.Fatal("acknowledgement payload was not delivered after capacity became available")
					}
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
		})
	}
}
