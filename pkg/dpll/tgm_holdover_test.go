package dpll

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	"github.com/stretchr/testify/require"
)

func stopTGMTestHoldover(t *testing.T, dpll *DpllConfig, events <-chan event.Event) {
	t.Helper()
	dpll.Lock()
	dpll.phaseOffset = 0
	dpll.phaseStatus, dpll.frequencyStatus = DPLL_LOCKED_HO_ACQ, DPLL_LOCKED_HO_ACQ
	dpll.stateDecision()
	dpll.Unlock()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		dpll.RLock()
		active := dpll.onHoldover
		dpll.RUnlock()
		if !active {
			return
		}
		select {
		case <-events:
		case <-deadline.C:
			t.Error("holdover worker did not finish cleanup")
			return
		}
	}
}

func TestTGMHoldoverWorkerHandoff(t *testing.T) {
	for _, renewedLoss := range []bool{false, true} {
		name := "stable recovery"
		if renewedLoss {
			name = "renewed loss"
		}
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			samples := make(chan int64, 8)
			option, err := WithHoldoverModel(holdoverModelFunc(func(time.Duration) (int64, error) {
				offset := int64(5)
				if calls.Add(1) > 1 {
					offset = 101
				}
				samples <- offset
				return offset, nil
			}))
			require.NoError(t, err)
			dpll := newEstimatorDPLL(t, event.GM, option)
			events := make(chan event.Event, 64)
			dpll.processConfig.EventChannel = events
			done := make(chan struct{})
			entered := make(chan struct{})
			t.Cleanup(func() {
				stopTGMTestHoldover(t, dpll, events)
			})
			dpll.Lock()
			dpll.onHoldover = true
			dpll.inSpec = false
			dpll.holdoverCloseCh = make(chan bool)
			go func() {
				close(entered)
				dpll.holdover()
				close(done)
			}()
			<-entered
			dpll.phaseOffset = 0
			dpll.phaseStatus, dpll.frequencyStatus = DPLL_LOCKED_HO_ACQ, DPLL_LOCKED_HO_ACQ
			dpll.stateDecision()
			if renewedLoss {
				for range 3 {
					dpll.phaseStatus, dpll.frequencyStatus = DPLL_HOLDOVER, DPLL_HOLDOVER
					dpll.stateDecision()
				}
			}
			dpll.Unlock()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("old worker did not finish the recovery handoff")
			}
			dpll.RLock()
			state, active, inSpec := dpll.state, dpll.onHoldover, dpll.inSpec
			dpll.RUnlock()
			require.True(t, inSpec, "recovery must clear the preceding out-of-spec latch")
			if !renewedLoss {
				require.Equal(t, event.PTP_LOCKED, state)
				require.False(t, active)
				require.Empty(t, samples)
				return
			}
			require.Equal(t, event.PTP_HOLDOVER, state, "latest hardware loss needs no additional notification")
			require.True(t, active)
			select {
			case offset := <-samples:
				require.Equal(t, int64(5), offset)
			case <-time.After(3 * time.Second):
				t.Fatal("renewed loss has no active estimator")
			}
			deadline := time.NewTimer(3 * time.Second)
			defer deadline.Stop()
			for {
				dpll.RLock()
				state, active = dpll.state, dpll.onHoldover
				dpll.RUnlock()
				if state == event.PTP_FREERUN && !active {
					require.Len(t, samples, 1, "terminal estimator sample must not restart the worker")
					dpll.Lock()
					for range 3 {
						dpll.stateDecision()
					}
					state, active, inSpec = dpll.state, dpll.onHoldover, dpll.inSpec
					dpll.Unlock()
					require.Equal(t, event.PTP_FREERUN, state)
					require.False(t, active)
					require.False(t, inSpec)
					return
				}
				select {
				case <-events:
				case <-deadline.C:
					t.Fatal("terminal error did not retire the renewed holdover worker")
				}
			}
		})
	}
}

func TestTGMHoldoverHandoffRetirementGuards(t *testing.T) {
	for _, reason := range []string{"Stop", "closed channel", "closing", "terminal freerun"} {
		t.Run(reason, func(t *testing.T) {
			var calls atomic.Int32
			option, err := WithHoldoverModel(holdoverModelFunc(func(time.Duration) (int64, error) {
				calls.Add(1)
				return 5, nil
			}))
			require.NoError(t, err)
			dpll := newEstimatorDPLL(t, event.GM, option)
			events := make(chan event.Event, 64)
			dpll.processConfig.EventChannel = events
			dpll.onHoldover = true
			dpll.state = event.PTP_LOCKED
			dpll.phaseStatus, dpll.frequencyStatus = DPLL_HOLDOVER, DPLL_HOLDOVER
			dpll.holdoverCloseCh = make(chan bool)
			dpll.processState = process.Running
			dpll.stopCh = make(chan struct{})
			switch reason {
			case "Stop":
				require.NoError(t, dpll.Stop())
			case "closed channel":
				close(dpll.holdoverCloseCh)
			case "closing":
				dpll.closing = true
			case "terminal freerun":
				dpll.state = event.PTP_FREERUN
				dpll.inSpec = false
				dpll.phaseOffset = FaultyPhaseOffset
			}
			t.Cleanup(func() { stopTGMTestHoldover(t, dpll, events) })
			done := make(chan struct{})
			go func() { dpll.holdover(); close(done) }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("retired worker failed to finish")
			}
			dpll.RLock()
			active, state, inSpec, offset := dpll.onHoldover, dpll.state, dpll.inSpec, dpll.phaseOffset
			dpll.RUnlock()
			require.False(t, active, "retirement must not restart an estimator")
			require.Zero(t, calls.Load())
			if reason == "terminal freerun" {
				require.Equal(t, event.PTP_FREERUN, state)
				require.False(t, inSpec)
				require.Equal(t, int64(FaultyPhaseOffset), offset)
			}
		})
	}
}

func TestTGMHoldoverThresholds(t *testing.T) {
	for _, sample := range []struct {
		name     string
		elapsed  time.Duration
		state    event.PTPState
		inSpec   bool
		terminal bool
	}{
		{"in-spec boundary", 40 * time.Second, event.PTP_HOLDOVER, true, false},
		{"out-of-spec", 41 * time.Second, event.PTP_HOLDOVER, false, false},
		{"freerun boundary", 100 * time.Second, event.PTP_HOLDOVER, false, false},
		{"terminal wins", 101 * time.Second, event.PTP_FREERUN, false, true},
	} {
		t.Run(sample.name, func(t *testing.T) {
			dpll := newEstimatorDPLL(t, event.GM)
			require.Equal(t, sample.terminal, dpll.advanceHoldover(sample.elapsed))
			require.Equal(t, sample.state, dpll.state)
			require.Equal(t, sample.inSpec, dpll.inSpec)
			if !sample.terminal {
				require.Equal(t, int64(sample.elapsed.Seconds()), dpll.phaseOffset)
			}
		})
	}
}

func TestTGMHoldoverTimeoutAndRepeatedHardware(t *testing.T) {
	model, err := NewLinearHoldoverModel(10, 100*time.Second)
	require.NoError(t, err)
	option, err := WithHoldoverModel(model)
	require.NoError(t, err)
	dpll := newEstimatorDPLL(t, event.GM, option)
	dpll.MaxInSpecOffset = 50
	dpll.onHoldover = true
	dpll.phaseStatus, dpll.frequencyStatus = DPLL_HOLDOVER, DPLL_HOLDOVER
	require.False(t, dpll.advanceHoldover(100*time.Second))
	require.True(t, dpll.inSpec)
	require.False(t, dpll.advanceHoldover(101*time.Second))
	require.False(t, dpll.inSpec)
	for range 3 {
		dpll.phaseOffset = 0
		dpll.stateDecision()
		require.Equal(t, event.PTP_HOLDOVER, dpll.state)
		require.False(t, dpll.inSpec)
		require.Equal(t, int64(10), dpll.phaseOffset)
	}
	dpll.phaseOffset = 0
	dpll.phaseStatus, dpll.frequencyStatus = DPLL_LOCKED_HO_ACQ, DPLL_LOCKED_HO_ACQ
	dpll.stateDecision()
	require.Equal(t, event.PTP_LOCKED, dpll.state)
	require.True(t, dpll.inSpec)
}

func TestTGMHoldoverRequiresAcquisitionAndEnabledTimeout(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		dpll := newEstimatorDPLL(t, event.GM)
		dpll.inSpec = disabled
		dpll.state = event.PTP_FREERUN
		if disabled {
			dpll.LocalHoldoverTimeout = 0
		}
		dpll.phaseStatus, dpll.frequencyStatus = DPLL_HOLDOVER, DPLL_HOLDOVER
		dpll.stateDecision()
		require.Equal(t, event.PTP_FREERUN, dpll.state)
		require.False(t, dpll.onHoldover)
	}
}

func TestTGMHoldoverEligibilityPreservesOtherHardwareAndSources(t *testing.T) {
	for _, sample := range []struct {
		name   string
		mode   event.ClockType
		source event.EventSource
		flags  Flag
	}{
		{"TBC", event.TBC, event.PTP4l, 0},
		{"GM with PTP source", event.GM, event.PTP4l, 0},
		{"GM without phase offset", event.GM, event.GNSS, FlagNoPhaseOffset},
	} {
		t.Run(sample.name, func(t *testing.T) {
			dpll := newEstimatorDPLL(t, sample.mode)
			dpll.dependsOn = []event.EventSource{sample.source}
			dpll.flags = sample.flags
			require.False(t, dpll.tgmHoldover())
		})
	}
}

func TestTGMHoldoverWorkerTimeoutAndRecovery(t *testing.T) {
	option, err := WithHoldoverModel(holdoverModelFunc(func(time.Duration) (int64, error) { return 5, nil }))
	require.NoError(t, err)
	dpll := newEstimatorDPLL(t, event.GM, option)
	dpll.LocalHoldoverTimeout = 1
	dpll.onHoldover = true
	dpll.holdoverCloseCh = make(chan bool)
	events := make(chan event.Event, 64)
	dpll.processConfig.EventChannel = events
	done := make(chan struct{})
	go func() { dpll.holdover(); close(done) }()
	t.Cleanup(func() {
		dpll.Lock()
		dpll.state = event.PTP_LOCKED
		dpll.Unlock()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("holdover worker failed to stop after recovery")
		}
	})
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case ev := <-events:
			data := ev.Data.(*event.DPLLData)
			if !data.OutOfSpec {
				continue
			}
			require.Equal(t, event.PTP_HOLDOVER, data.State)
			require.NotNil(t, data.Offset)
			require.Equal(t, int64(5), *data.Offset)
			dpll.Lock()
			dpll.phaseOffset = 0
			dpll.phaseStatus, dpll.frequencyStatus = DPLL_LOCKED_HO_ACQ, DPLL_LOCKED_HO_ACQ
			dpll.stateDecision()
			dpll.Unlock()
			select {
			case <-done:
			case <-deadline.C:
				t.Fatal("holdover worker did not yield to recovery")
			}
			dpll.Lock()
			require.Equal(t, event.PTP_LOCKED, dpll.state)
			require.True(t, dpll.inSpec)
			dpll.Unlock()
			return
		case <-deadline.C:
			t.Fatal("holdover worker did not report timeout quality")
		}
	}
}
