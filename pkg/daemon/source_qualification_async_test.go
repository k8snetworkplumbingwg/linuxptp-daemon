package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clockmgr"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	parserconstants "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/require"
)

const sourceTestConfig = "ptp4l.0.config"

// newSourceTestProcess connects a parser and a T-BC clock without real hardware.
func newSourceTestProcess(t *testing.T) (*ptpProcess, *clockmgr.ClockManager) {
	t.Helper()
	InitializeOffsetMaps()
	t.Cleanup(InitializeOffsetMaps)
	profile := &ptpv1.PtpProfile{Name: utils.ToPtr("source-test"), PtpSettings: map[string]string{
		"clockType": TBC, "leadingInterface": "eno5", "clockId[eno5]": "123456789", "inSyncConditionThreshold": "100",
	}, PtpClockThreshold: &ptpv1.PtpClockThreshold{PtpSourceQualifiedSamples: utils.ToPtr(int64(1))}}
	events := make(chan event.Event, 100)
	cm := clockmgr.Init("test", events, nil, nil, nil, nil)
	clk, err := clock.NewTBC(sourceTestConfig, cm.GetUtcOffset, &pmc.MockClient{}, "ptp4l.1.config")
	require.NoError(t, err)
	clk.SetConfiguredLeadingInterface("eno5")
	require.NoError(t, cm.AddClock(clk))
	p := &ptpProcess{
		ExecProcess: ExecProcess{name: event.Ptp4lProcessName, configName: sourceTestConfig, messageTag: "[ptp4l.0.config:{level}]", eventCh: events},
		nodeProfile: profile, clockType: event.TBC, logParser: parser.NewPTP4LExtractor(), ptpClockThreshold: getPTPThreshold(profile),
		ifaces:        config.IFaces{{Name: "tr0"}, {Name: "tr1"}},
		tBCAttributes: tBCProcessAttributes{trPortsConfigFile: sourceTestConfig, trIfaceNames: []string{"tr0", "tr1"}, offsetEventWindow: utils.NewWindow(1)},
		dn:            &Daemon{processManager: &ProcessManager{clockMgr: cm}},
		pm:            &plugin.PluginManager{Plugins: map[string]*plugin.Plugin{}},
	}
	policy, err := resolveSourceQualificationPolicy(profile)
	require.NoError(t, err)
	require.NoError(t, p.configureSourceQualification(policy))
	p.sourceQualification.ctx = startQualificationClock(t, cm, events)
	t.Cleanup(p.sourceQualification.close)
	return p, cm
}

func sourceLine(message string) string {
	return fmt.Sprintf("ptp4l[1.123]: [%s:5] %s", sourceTestConfig, message)
}

func TestSourceObservationDoesNotWaitForProfileApply(t *testing.T) {
	// A selected-port role must be retained during apply without blocking the scanner.
	p, cm := newSourceTestProcess(t)
	cm.SetApplying(true)
	t.Cleanup(func() { cm.SetApplying(false) })
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.processOutput(sourceLine("port 1: UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED"))
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scanner waited for profile apply")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := waitForQualificationSnapshot(ctx, p, cm)
	require.NoError(t, err)
	before, _ := cm.SourceQualification(sourceTestConfig)
	require.Empty(t, before.IFace)
	cm.SetApplying(false)
	after, _ := cm.SourceQualification(sourceTestConfig)
	require.Equal(t, "tr0", after.IFace, "the selected role must survive profile apply")
}

func TestSourceForwardingUsesConfiguredClockID(t *testing.T) {
	// The clock needs the local identity in source observations for announcements.
	p, _ := newSourceTestProcess(t)
	p.eventCh = make(chan event.Event, 1)
	p.forwardSourceData(nil, &parser.PTPEvent{Iface: "tr0", Role: parserconstants.PortRoleSlave})
	observation := (<-p.eventCh).Data.(*event.PTPSourceObservation)
	clockID, err := p.getPTPClockID()
	require.NoError(t, err)
	require.Equal(t, clockID, observation.ClockID)
}

func TestScannerReadsWhileSourceInputActionIsBlocked(t *testing.T) {
	// A real pipe verifies that stdout is still drained while hardware is blocked.
	p, _ := newSourceTestProcess(t)
	started := make(chan struct{})
	release := make(chan struct{})
	stop := make(chan struct{})
	var releaseOnce sync.Once
	var startOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	qlf := p.sourceQualification
	// The test and action worker share the configured callback.
	qlf.mu.Lock()
	qlf.apply = func(bool) error {
		startOnce.Do(func() { close(started); <-release })
		return nil
	}
	qlf.mu.Unlock()
	reader, writer := io.Pipe()
	p.runScanner(reader)
	written := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		if _, err := fmt.Fprintln(writer, sourceLine("port 1: UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED")); err != nil {
			return
		}
		select {
		case <-started:
		case <-stop:
			return
		}
		if _, err := fmt.Fprintln(writer, sourceLine("unrelated diagnostic")); err == nil {
			close(written)
		}
		_ = writer.Close()
	}()
	t.Cleanup(func() {
		close(stop)
		finish()
		qlf.close()
		_ = reader.Close()
		_ = writer.Close()
		<-writerDone
		p.wg.Wait()
	})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("input action did not start")
	}
	select {
	case <-written:
	case <-time.After(time.Second):
		t.Fatal("scanner stopped reading while hardware was blocked")
	}
}

func TestSourceLossReachesClockWhileHardwareIsBlocked(t *testing.T) {
	// A blocked enable must not delay a subsequent source-loss decision.
	p, cm := newSourceTestProcess(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	var startOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(finish)
	qlf := p.sourceQualification
	// The test and action worker share the configured callback.
	qlf.mu.Lock()
	qlf.apply = func(qualified bool) error {
		if qualified {
			startOnce.Do(func() { close(started); <-release })
		}
		return nil
	}
	qlf.mu.Unlock()
	p.processOutput(sourceLine("port 1: UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED"))
	p.processOutput(sourceLine("master offset 5 s2 freq 0 path delay 100"))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("input enable did not start")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.processOutput(sourceLine("port 1: SLAVE to MASTER on ANNOUNCE_RECEIPT_TIMEOUT_EXPIRES"))
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scanner waited for the hardware action")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stats, err := waitForQualificationSnapshot(ctx, p, cm)
	require.NoError(t, err)
	require.False(t, stats.SourceQualified[sourceTestConfig])
}

func TestAggregateRecoveryFollowsDPLLNotHardwareResult(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Even after a failed enable and 16 seconds of virtual time, aggregate
		// recovery requires the DPLL to report LOCKED and the offsets to converge.
		p, cm := newSourceTestProcess(t)
		pushDPLL := func(state event.PTPState) {
			cm.ProcessEvent(event.Event{Source: event.DPLL, CfgName: sourceTestConfig, IFace: "eno5", ClockType: event.TBC, Time: time.Now().UnixMilli(), Data: &event.DPLLData{
				State: state, LeadingSource: true, Offset: utils.ToPtr(int64(5)), InSyncConditionThreshold: 100,
				InSyncConditionTimes: 1, ToFreeRunThreshold: 1500, MaxInSpecOffset: 500,
			}})
		}
		pushDPLL(event.PTP_HOLDOVER)
		// The test and action worker share the configured callback.
		p.sourceQualification.mu.Lock()
		p.sourceQualification.apply = func(qualified bool) error {
			if qualified {
				return errors.New("simulated partial enable failure")
			}
			return nil
		}
		p.sourceQualification.mu.Unlock()
		p.processOutput(sourceLine("port 1: UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED"))
		p.processOutput(sourceLine("master offset 5 s2 freq 0 path delay 100"))
		require.NotEqual(t, event.PTP_LOCKED, qualificationSnapshot(t, p, cm).ClockStates[sourceTestConfig])
		time.Sleep(16 * time.Second)
		for i := 0; i <= event.WindowSize; i++ {
			cm.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: sourceTestConfig, IFace: "tr0", ClockType: event.TBC,
				Time: time.Now().UnixMilli(), Data: &event.OffsetData{State: event.PTP_LOCKED, Offset: 5}})
			pushDPLL(event.PTP_HOLDOVER)
		}
		require.NotEqual(t, event.PTP_LOCKED, qualificationSnapshot(t, p, cm).ClockStates[sourceTestConfig])
		pushDPLL(event.PTP_LOCKED)
		require.Equal(t, event.PTP_LOCKED, qualificationSnapshot(t, p, cm).ClockStates[sourceTestConfig])
	})
}

func TestSelectedPortLossImmediatelyUpdatesAggregate(t *testing.T) {
	// Start with healthy PTP/DPLL data, then lose the selected port. The clock
	// must enter holdover immediately and queue the loss action in order.
	p, cm := newSourceTestProcess(t)
	actions := make(chan bool, 3)
	qlf := p.sourceQualification
	// The test and action worker share the configured callback.
	qlf.mu.Lock()
	qlf.apply = func(qualified bool) error { actions <- qualified; return nil }
	qlf.mu.Unlock()
	p.processOutput(sourceLine("port 1: UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED"))
	p.processOutput(sourceLine("master offset 5 s2 freq 0 path delay 100"))
	require.True(t, qualificationSnapshot(t, p, cm).SourceQualified[sourceTestConfig])
	for i := 0; i <= event.WindowSize; i++ {
		now := time.Now().UnixMilli()
		cm.ProcessEvent(event.Event{Source: event.DPLL, CfgName: sourceTestConfig, IFace: "eno5", Time: now, Data: &event.DPLLData{
			State: event.PTP_LOCKED, LeadingSource: true, Offset: utils.ToPtr(int64(5)),
			InSyncConditionThreshold: 100, InSyncConditionTimes: 1, ToFreeRunThreshold: 1500, MaxInSpecOffset: 500,
		}})
		cm.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: sourceTestConfig, IFace: "tr0", Time: now,
			Data: &event.OffsetData{Offset: 5}})
	}
	require.Equal(t, event.PTP_LOCKED, qualificationSnapshot(t, p, cm).ClockStates[sourceTestConfig])
	p.processOutput(sourceLine("port 1: SLAVE to MASTER on ANNOUNCE_RECEIPT_TIMEOUT_EXPIRES"))
	stats := qualificationSnapshot(t, p, cm)
	require.False(t, stats.SourceQualified[sourceTestConfig])
	require.Equal(t, event.PTP_HOLDOVER, stats.ClockStates[sourceTestConfig], "port loss must not wait for another offset")
	require.Len(t, actions, 3)
	require.Equal(t, []bool{false, true, false}, []bool{<-actions, <-actions, <-actions})
}
