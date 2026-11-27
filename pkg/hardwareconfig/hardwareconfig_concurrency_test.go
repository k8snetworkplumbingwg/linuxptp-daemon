package hardwareconfig

import (
	"runtime"
	"sync"
	"testing"
	"time"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
	"github.com/stretchr/testify/require"
)

func TestApplyConditionConcurrentHardwareConfigUpdate(t *testing.T) {
	// Exercise cache replacement and condition lookup concurrently under the
	// race detector, without contacting hardware.
	hcm := NewHardwareConfigManager(nil, "", nil)
	name := "test-profile"
	profile := &ptpv1.PtpProfile{Name: &name}
	start := make(chan struct{})
	done := make(chan error, 2)
	go func() {
		<-start
		for i := 0; i < 1000; i++ {
			if err := hcm.UpdateHardwareConfig(nil); err != nil {
				done <- err
				return
			}
			runtime.Gosched()
		}
		done <- nil
	}()
	go func() {
		<-start
		for i := 0; i < 1000; i++ {
			if err := hcm.ApplyConditionForProfile(profile, ConditionTypeLost); err != nil {
				done <- err
				return
			}
			runtime.Gosched()
		}
		done <- nil
	}()
	close(start)
	require.NoError(t, <-done)
	require.NoError(t, <-done)
}

func TestApplyConditionUsesSnapshotDuringHardwareConfigUpdate(t *testing.T) {
	// Block the fake writer after lookup: the cache must remain replaceable while
	// the action finishes using its already-captured commands.
	hcm := NewHardwareConfigManager(nil, "", nil)
	name := "prefix_test-profile"
	profile := &ptpv1.PtpProfile{Name: &name}
	hcm.setHardwareConfigs([]enrichedHardwareConfig{{
		HardwareConfig: ptpv2alpha1.HardwareConfig{Spec: ptpv2alpha1.HardwareConfigSpec{
			RelatedPtpProfileName: "test-profile",
			Profile: ptpv2alpha1.HardwareProfile{ClockChain: &ptpv2alpha1.ClockChain{Behavior: &ptpv2alpha1.Behavior{
				Conditions: []ptpv2alpha1.Condition{{Name: "lost", Triggers: []ptpv2alpha1.SourceState{{ConditionType: ConditionTypeLost}}}},
			}}},
		}},
		sysFSCommands: map[string][]SysFSCommand{"lost": {{Path: "pin", Value: "disabled"}}},
	}})
	started := make(chan SysFSCommand, 1)
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	t.Cleanup(finish)
	hcm.overrideExecutors(nil, func(path, value string) error {
		started <- SysFSCommand{Path: path, Value: value}
		<-release
		return nil
	})
	done := make(chan error, 1)
	go func() { done <- hcm.ApplyConditionForProfile(profile, ConditionTypeLost) }()
	select {
	case command := <-started:
		require.Equal(t, "pin", command.Path)
		require.Equal(t, "disabled", command.Value)
	case <-time.After(time.Second):
		t.Fatal("hardware action did not start")
	}
	updated := make(chan struct{})
	go func() { hcm.setHardwareConfigs(nil); close(updated) }()
	select {
	case <-updated:
	case <-time.After(time.Second):
		t.Fatal("config update blocked on hardware I/O")
	}
	finish()
	require.NoError(t, <-done)
}
