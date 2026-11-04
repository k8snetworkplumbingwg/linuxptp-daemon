package dpll

import (
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	"github.com/stretchr/testify/require"
)

type holdoverModelFunc func(time.Duration) (int64, error)

func (model holdoverModelFunc) Estimate(elapsed time.Duration) (int64, error) {
	return model(elapsed)
}

func newModeledDPLL(t *testing.T) *DpllConfig {
	t.Helper()
	dpll := &DpllConfig{
		LocalMaxHoldoverOffSet: 100,
		LocalHoldoverTimeout:   100,
		MaxInSpecOffset:        40,
		dependsOn:              []event.EventSource{event.GNSS},
		phaseStatus:            DPLL_LOCKED_HO_ACQ,
		frequencyStatus:        DPLL_LOCKED_HO_ACQ,
		processConfig: config.ProcessConfig{
			ClockType: event.GM, ConfigName: "ts2phc.0.config", EventChannel: make(chan event.Event, 256),
		},
	}
	t.Cleanup(func() {
		dpll.Lock()
		dpll.endModeledHoldover()
		dpll.Unlock()
		dpll.wg.Wait()
	})
	return dpll
}

func setModeledHardware(dpll *DpllConfig, state int64) {
	dpll.Lock()
	defer dpll.Unlock()
	dpll.phaseStatus, dpll.frequencyStatus = state, state
	dpll.stateDecision()
}

func prepareModeledEpisode(dpll *DpllConfig, model HoldoverModel) {
	dpll.holdoverModel = model
	dpll.holdoverAcquired = true
	dpll.onHoldover = true
	dpll.state = event.PTP_HOLDOVER
	dpll.inSpec = true
	dpll.holdoverStart = time.Now()
	dpll.holdoverGeneration++
}

func TestTGMHoldoverDecision(t *testing.T) {
	for _, sample := range []struct {
		name    string
		offset  int64
		elapsed time.Duration
		inSpec  bool
		state   event.PTPState
	}{
		{"below in-spec", 39, time.Second, true, event.PTP_HOLDOVER},
		{"equal in-spec", 40, time.Second, true, event.PTP_HOLDOVER},
		{"above in-spec", 41, time.Second, false, event.PTP_HOLDOVER},
		{"below freerun", 99, time.Second, false, event.PTP_HOLDOVER},
		{"equal freerun", 100, time.Second, false, event.PTP_HOLDOVER},
		{"above freerun", 101, time.Second, false, event.PTP_FREERUN},
		{"before timeout", 10, 100*time.Second - time.Nanosecond, true, event.PTP_HOLDOVER},
		{"equal timeout", 10, 100 * time.Second, true, event.PTP_HOLDOVER},
		{"after timeout", 10, 100*time.Second + time.Nanosecond, false, event.PTP_HOLDOVER},
		{"simultaneous", 101, 101 * time.Second, false, event.PTP_FREERUN},
	} {
		t.Run(sample.name, func(t *testing.T) {
			dpll := newModeledDPLL(t)
			prepareModeledEpisode(dpll, holdoverModelFunc(func(time.Duration) (int64, error) { return sample.offset, nil }))
			dpll.evaluateModeledHoldover(dpll.holdoverGeneration, dpll.holdoverStart.Add(sample.elapsed))
			require.Equal(t, sample.state, receiveDPLLObservation(t, dpll).State)
			require.Equal(t, sample.inSpec, !receiveDPLLObservation(t, dpll).OutOfSpec)
		})
	}
}

func TestTGMEqualOrHigherInSpecLimit(t *testing.T) {
	for _, limit := range []uint64{100, 200} {
		dpll := newModeledDPLL(t)
		dpll.MaxInSpecOffset = limit
		prepareModeledEpisode(dpll, holdoverModelFunc(func(time.Duration) (int64, error) { return 101, nil }))
		dpll.evaluateModeledHoldover(dpll.holdoverGeneration, dpll.holdoverStart.Add(time.Second))
		require.Equal(t, event.PTP_FREERUN, receiveDPLLObservation(t, dpll).State)
	}
}

func TestTGMResolvedHoldoverConfiguration(t *testing.T) {
	for _, sample := range []struct {
		name    string
		timeout uint64
		offset  uint64
	}{
		{"disabled", 0, 100}, {"disabled zero limits", 0, 0},
		{"invalid linear", 100, 0}, {"duration overflow", math.MaxUint64, 100},
	} {
		t.Run(sample.name, func(t *testing.T) {
			dpll := newModeledDPLL(t)
			dpll.LocalHoldoverTimeout, dpll.LocalMaxHoldoverOffSet = sample.timeout, sample.offset
			setModeledHardware(dpll, DPLL_LOCKED_HO_ACQ)
			require.Equal(t, event.PTP_LOCKED, receiveDPLLObservation(t, dpll).State)
			require.Nil(t, dpll.holdoverModel)
			setModeledHardware(dpll, DPLL_HOLDOVER)
			require.Equal(t, event.PTP_FREERUN, receiveDPLLObservation(t, dpll).State)
			if sample.timeout == 0 {
				require.Nil(t, dpll.holdoverModel)
				require.False(t, dpll.onHoldover)
			}
			require.NoError(t, dpll.ReplaceHoldoverModel(holdoverModelFunc(func(time.Duration) (int64, error) { return 0, nil })))
			setModeledHardware(dpll, DPLL_LOCKED_HO_ACQ)
			setModeledHardware(dpll, DPLL_HOLDOVER)
			require.Equal(t, event.PTP_FREERUN, receiveDPLLObservation(t, dpll).State)
		})
	}
}

func TestTGMModelDoesNotRequireMeasuredOffset(t *testing.T) {
	dpll := newModeledDPLL(t)
	dpll.flags = FlagNoPhaseOffset
	events := make(chan event.Event, 16)
	dpll.processConfig.EventChannel = events
	model, err := NewLinearHoldoverModel(100, 100*time.Second)
	require.NoError(t, err)
	prepareModeledEpisode(dpll, model)
	dpll.evaluateModeledHoldover(dpll.holdoverGeneration, dpll.holdoverStart.Add(50*time.Second))
	require.True(t, receiveDPLLObservation(t, dpll).OutOfSpec)
	require.Equal(t, event.PTP_HOLDOVER, receiveDPLLObservation(t, dpll).State)
	ev := <-events
	require.Nil(t, ev.Data.(*event.DPLLData).Offset)
	require.True(t, ev.Data.(*event.DPLLData).Holdover.Active)
	dpll.evaluateModeledHoldover(dpll.holdoverGeneration, dpll.holdoverStart.Add(101*time.Second))
	require.Equal(t, event.PTP_FREERUN, receiveDPLLObservation(t, dpll).State)
}

func TestTGMModeledProvenanceDoesNotReplaceMeasurementOrMarkHardware(t *testing.T) {
	for _, flags := range []Flag{0, FlagNoPhaseOffset, FlagOnlyPhaseStatus} {
		dpll := newModeledDPLL(t)
		events := make(chan event.Event, 16)
		dpll.processConfig.EventChannel = events
		dpll.flags = flags
		dpll.phaseOffset = 500
		prepareModeledEpisode(dpll, holdoverModelFunc(func(time.Duration) (int64, error) { return 50, nil }))
		dpll.modeledOffset = 50
		dpll.sendDpllEvent()
		sample := (<-events).Data.(*event.DPLLData)
		require.False(t, sample.ModeledHoldover)
		require.Equal(t, flags == FlagOnlyPhaseStatus, sample.Holdover.PhaseOnly)
		if dpll.hasFlag(FlagNoPhaseOffset) {
			require.Nil(t, sample.Offset)
		} else {
			require.Equal(t, int64(500), *sample.Offset)
		}
		dpll.endModeledHoldover()
		dpll.sendDpllEvent()
		require.False(t, (<-events).Data.(*event.DPLLData).ModeledHoldover)
	}
}

func TestTGMReplaceModelPreservesEpisode(t *testing.T) {
	dpll := newModeledDPLL(t)
	prepareModeledEpisode(dpll, holdoverModelFunc(func(time.Duration) (int64, error) { return 50, nil }))
	start, generation := dpll.holdoverStart, dpll.holdoverGeneration
	dpll.evaluateModeledHoldover(generation, start.Add(time.Second))
	require.True(t, receiveDPLLObservation(t, dpll).OutOfSpec)
	var gotElapsed time.Duration
	require.NoError(t, dpll.ReplaceHoldoverModel(holdoverModelFunc(func(elapsed time.Duration) (int64, error) {
		gotElapsed = elapsed
		return 10, nil
	})))
	dpll.evaluateModeledHoldover(generation, start.Add(2*time.Second))
	require.Equal(t, 2*time.Second, gotElapsed)
	require.Equal(t, start, dpll.holdoverStart)
	require.True(t, receiveDPLLObservation(t, dpll).OutOfSpec)
	require.Equal(t, event.PTP_HOLDOVER, receiveDPLLObservation(t, dpll).State)
	var nilModel holdoverModelFunc
	var nilPointer *linearHoldoverModel
	revision := dpll.modelRevision
	require.Error(t, dpll.ReplaceHoldoverModel(nil))
	require.Error(t, dpll.ReplaceHoldoverModel(nilModel))
	require.Error(t, dpll.ReplaceHoldoverModel(nilPointer))
	require.Equal(t, revision, dpll.modelRevision)
	setModeledHardware(dpll, DPLL_LOCKED_HO_ACQ)
	require.Equal(t, event.PTP_LOCKED, receiveDPLLObservation(t, dpll).State)
	require.False(t, receiveDPLLObservation(t, dpll).OutOfSpec)
	setModeledHardware(dpll, DPLL_HOLDOVER)
	dpll.Lock()
	require.WithinDuration(t, time.Now(), dpll.holdoverStart, time.Second)
	dpll.Unlock()
}

func TestTGMRejectsStaleModelResults(t *testing.T) {
	for _, action := range []string{"replace", "recover", "freerun", "reset"} {
		for _, modelErr := range []error{nil, errors.New("old model error")} {
			t.Run(action+"/"+fmtError(modelErr), func(t *testing.T) {
				dpll := newModeledDPLL(t)
				entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
				prepareModeledEpisode(dpll, holdoverModelFunc(func(time.Duration) (int64, error) {
					close(entered)
					<-release
					return 101, modelErr
				}))
				generation, start := dpll.holdoverGeneration, dpll.holdoverStart
				go func() {
					defer close(done)
					dpll.evaluateModeledHoldover(generation, start.Add(time.Second))
				}()
				<-entered
				switch action {
				case "replace":
					require.NoError(t, dpll.ReplaceHoldoverModel(holdoverModelFunc(func(time.Duration) (int64, error) { return 10, nil })))
				case "recover":
					setModeledHardware(dpll, DPLL_LOCKED_HO_ACQ)
				case "freerun":
					setModeledHardware(dpll, DPLL_FREERUN)
				case "reset":
					dpll.Lock()
					dpll.endModeledHoldover()
					dpll.Unlock()
				}
				close(release)
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("stale evaluation did not finish")
				}
				switch action {
				case "replace":
					require.Equal(t, event.PTP_HOLDOVER, receiveDPLLObservation(t, dpll).State)
					require.False(t, receiveDPLLObservation(t, dpll).OutOfSpec)
				case "recover":
					require.Equal(t, event.PTP_LOCKED, receiveDPLLObservation(t, dpll).State)
				}
			})
		}
	}
}

func fmtError(err error) string {
	if err == nil {
		return "success"
	}
	return "error"
}

func TestTGMModelErrorsFailClosed(t *testing.T) {
	for _, model := range []HoldoverModel{
		holdoverModelFunc(func(time.Duration) (int64, error) { return -1, nil }),
		holdoverModelFunc(func(time.Duration) (int64, error) { return 0, errors.New("model failed") }),
	} {
		dpll := newModeledDPLL(t)
		prepareModeledEpisode(dpll, model)
		dpll.evaluateModeledHoldover(dpll.holdoverGeneration, dpll.holdoverStart.Add(time.Second))
		require.Equal(t, event.PTP_FREERUN, receiveDPLLObservation(t, dpll).State)
	}
}

func TestTGMReplacementScopeAndUnacquiredStartup(t *testing.T) {
	dpll := newModeledDPLL(t)
	setModeledHardware(dpll, DPLL_HOLDOVER)
	require.Equal(t, event.PTP_FREERUN, receiveDPLLObservation(t, dpll).State)
	require.Nil(t, dpll.holdoverModel)
	dpll.flags = FlagOnlyPhaseStatus
	require.False(t, dpll.supportsModelReplacement())
	require.Error(t, dpll.ReplaceHoldoverModel(holdoverModelFunc(func(time.Duration) (int64, error) { return 0, nil })))
	dpll.flags = 0
	dpll.processConfig.ClockType = event.TBC
	require.False(t, dpll.supportsModelReplacement())
}

func TestTGMUsesInjectedModelAcrossTriggers(t *testing.T) {
	dpll := newModeledDPLL(t)
	var callsMu sync.Mutex
	var calls []time.Duration
	require.NoError(t, dpll.ReplaceHoldoverModel(holdoverModelFunc(func(elapsed time.Duration) (int64, error) {
		callsMu.Lock()
		calls = append(calls, elapsed)
		callsMu.Unlock()
		return 50, nil
	})))
	setModeledHardware(dpll, DPLL_LOCKED_HO_ACQ)
	setModeledHardware(dpll, DPLL_HOLDOVER)
	require.Eventually(t, func() bool {
		callsMu.Lock()
		defer callsMu.Unlock()
		return len(calls) >= 1
	}, time.Second, time.Millisecond)
	setModeledHardware(dpll, DPLL_HOLDOVER)
	require.Eventually(t, func() bool {
		callsMu.Lock()
		defer callsMu.Unlock()
		return len(calls) >= 2
	}, time.Second, time.Millisecond)
	require.Eventually(t, func() bool {
		callsMu.Lock()
		defer callsMu.Unlock()
		return len(calls) >= 3
	}, 2*time.Second, time.Millisecond)
	require.Equal(t, event.PTP_HOLDOVER, receiveDPLLObservation(t, dpll).State)
	require.True(t, receiveDPLLObservation(t, dpll).OutOfSpec)
	dpll.RLock()
	require.Zero(t, dpll.phaseOffset)
	dpll.RUnlock()
}

func TestTGMHoldoverEpisodeReset(t *testing.T) {
	for _, offset := range []int64{10, 50} {
		t.Run(fmtError(nil)+time.Duration(offset).String(), func(t *testing.T) {
			dpll := newModeledDPLL(t)
			prepareModeledEpisode(dpll, holdoverModelFunc(func(time.Duration) (int64, error) { return offset, nil }))
			oldStart, oldGeneration := dpll.holdoverStart, dpll.holdoverGeneration
			dpll.evaluateModeledHoldover(oldGeneration, oldStart.Add(time.Second))
			require.Equal(t, offset <= 40, !receiveDPLLObservation(t, dpll).OutOfSpec)
			setModeledHardware(dpll, DPLL_LOCKED_HO_ACQ)
			require.Equal(t, event.PTP_LOCKED, receiveDPLLObservation(t, dpll).State)
			require.False(t, receiveDPLLObservation(t, dpll).OutOfSpec)
			require.Zero(t, dpll.modeledOffset)
			require.True(t, dpll.holdoverStart.IsZero())
			setModeledHardware(dpll, DPLL_HOLDOVER)
			dpll.Lock()
			require.Greater(t, dpll.holdoverGeneration, oldGeneration)
			require.True(t, dpll.holdoverStart.After(oldStart))
			require.WithinDuration(t, time.Now(), dpll.holdoverStart, time.Second)
			dpll.Unlock()
		})
	}
}

func TestTGMStopRejectsInflightErrorAndLateHardware(t *testing.T) {
	dpll := newModeledDPLL(t)
	entered, release, stopped := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	require.NoError(t, dpll.ReplaceHoldoverModel(holdoverModelFunc(func(time.Duration) (int64, error) {
		close(entered)
		<-release
		return 0, errors.New("late model error")
	})))
	setModeledHardware(dpll, DPLL_LOCKED_HO_ACQ)
	setModeledHardware(dpll, DPLL_HOLDOVER)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("model did not start")
	}
	go func() { stopped <- dpll.Stop() }()
	require.Eventually(t, func() bool { return dpll.State() == process.Stopped }, time.Second, time.Millisecond)
	setModeledHardware(dpll, DPLL_LOCKED_HO_ACQ)
	setModeledHardware(dpll, DPLL_HOLDOVER)
	dpll.RLock()
	require.False(t, dpll.onHoldover)
	require.False(t, dpll.holdoverAcquired)
	dpll.RUnlock()
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-stopped:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("stop did not wait for canceled evaluation")
	}
	require.Equal(t, event.PTP_FREERUN, receiveDPLLObservation(t, dpll).State)
}
