package dpll

import (
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/require"
)

func newEstimatorDPLL(t *testing.T, mode event.ClockType, source event.EventSource, options ...Option) *DpllConfig {
	t.Helper()
	dpll := NewDpll(1, 100, 100, 40, "estimator-test", []event.EventSource{source}, MOCK, nil,
		0, 0, 0, config.ProcessConfig{ClockType: mode, EventChannel: make(chan event.Event, 256)}, nil, options...)
	dpll.phaseOffset = 3
	t.Cleanup(func() {
		dpll.ticker.Stop()
		dpll.Lock()
		dpll.endModeledHoldover()
		dpll.Unlock()
		dpll.wg.Wait()
	})
	return dpll
}

func TestConstructorEstimatorDrivesBothHoldoverWorkers(t *testing.T) {
	for _, mode := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(mode), func(t *testing.T) {
			source := event.GNSS
			if mode == event.TBC {
				source = event.PTP4l
			}
			option, err := WithHoldoverModel(holdoverModelFunc(func(elapsed time.Duration) (int64, error) {
				return 50 + int64(elapsed.Seconds()*elapsed.Seconds()), nil
			}))
			require.NoError(t, err)
			dpll := newEstimatorDPLL(t, mode, source, option)
			events := make(chan event.Event, 256)
			dpll.processConfig.EventChannel = events
			setModeledHardware(dpll, DPLL_LOCKED_HO_ACQ)
			setModeledHardware(dpll, DPLL_HOLDOVER)
			deadline := time.NewTimer(time.Second)
			defer deadline.Stop()
			for {
				select {
				case ev := <-events:
					data := ev.Data.(*event.DPLLData)
					if !data.Holdover.Valid {
						continue
					}
					require.Equal(t, event.PTP_HOLDOVER, data.State)
					require.False(t, data.OutOfSpec)
					require.Equal(t, int64(3), *data.Offset)
					require.GreaterOrEqual(t, data.Holdover.EstimatedErrorNS, int64(50))
					received := receiveDPLLWire(t, mode, data)
					expectedState := event.PTP_HOLDOVER
					if mode == event.GM {
						expectedState = event.PTP_FREERUN
					}
					require.Equal(t, expectedState, received.State)
					require.True(t, received.OutOfSpec)
					if mode == event.GM {
						require.Equal(t, int64(FaultyPhaseOffset), received.Offset)
					} else {
						require.Equal(t, data.Holdover.EstimatedErrorNS, received.Offset)
					}
					return
				case <-deadline.C:
					t.Fatal("nonlinear estimator did not publish an observation")
				}
			}
		})
	}
}

func TestNonlinearObservationThroughManagerAtKnownElapsed(t *testing.T) {
	for _, mode := range []event.ClockType{event.GM, event.TBC} {
		source := event.GNSS
		if mode == event.TBC {
			source = event.PTP4l
		}
		option, err := WithHoldoverModel(holdoverModelFunc(func(elapsed time.Duration) (int64, error) {
			return 50 + int64(elapsed.Seconds()*elapsed.Seconds()), nil
		}))
		require.NoError(t, err)
		dpll := newEstimatorDPLL(t, mode, source, option)
		events := make(chan event.Event, 16)
		dpll.processConfig.EventChannel = events
		prepareModeledEpisode(dpll)
		dpll.evaluateModeledHoldover(dpll.holdoverGeneration, dpll.holdoverStart.Add(3*time.Second))
		raw := (<-events).Data.(*event.DPLLData)
		require.Equal(t, int64(59), raw.Holdover.EstimatedErrorNS)
		require.True(t, raw.Holdover.Valid)
		require.Equal(t, int64(3), *raw.Offset)
		require.False(t, raw.OutOfSpec)
		received := receiveDPLLWire(t, mode, raw)
		require.True(t, received.OutOfSpec)
		expectedState := event.PTP_FREERUN
		if mode == event.TBC {
			expectedState = event.PTP_HOLDOVER
		}
		require.Equal(t, expectedState, received.State)
		expected := int64(FaultyPhaseOffset)
		if mode == event.TBC {
			expected = 59
		}
		require.Equal(t, expected, received.Offset)
	}
}

func TestSharedDefaultEstimateRoundingAndModePolicy(t *testing.T) {
	for _, elapsed := range []time.Duration{0, 1499 * time.Millisecond, 1500 * time.Millisecond, 40 * time.Second, 40500 * time.Millisecond, 100 * time.Second, 101 * time.Second} {
		for _, mode := range []event.ClockType{event.GM, event.TBC} {
			source := event.GNSS
			if mode == event.TBC {
				source = event.PTP4l
			}
			dpll := newEstimatorDPLL(t, mode, source)
			prepareModeledEpisode(dpll)
			dpll.evaluateModeledHoldover(dpll.holdoverGeneration, dpll.holdoverStart.Add(elapsed))
			expected := int64(math.Round(elapsed.Seconds()))
			require.Equal(t, expected, dpll.modeledOffset)
			require.Equal(t, int64(3), dpll.phaseOffset)
			received := receiveDPLLObservation(t, dpll)
			terminal := expected > 40 || elapsed >= 100*time.Second
			if mode == event.TBC {
				terminal = elapsed >= 100*time.Second
			}
			if terminal {
				require.Equal(t, event.PTP_FREERUN, received.State)
			} else {
				require.Equal(t, event.PTP_HOLDOVER, received.State)
				require.Equal(t, expected > 40, received.OutOfSpec)
			}
		}
	}
}

func TestSharedEstimatorInjectionValidation(t *testing.T) {
	var typedNil holdoverModelFunc
	var nilPointer *linearHoldoverModel
	for _, model := range []HoldoverModel{nil, typedNil, nilPointer} {
		option, err := WithHoldoverModel(model)
		require.Error(t, err)
		require.Nil(t, option)
	}
}

func TestLegacyEstimatorErrorsAndUnsignedLimits(t *testing.T) {
	for _, sample := range []struct {
		name               string
		maxOffset, timeout uint64
		elapsed            time.Duration
		model              HoldoverModel
		terminal           bool
	}{
		{name: "zero drift", timeout: 100, elapsed: time.Second},
		{name: "disabled", maxOffset: 100, elapsed: time.Second, terminal: true},
		{name: "negative elapsed", maxOffset: 100, timeout: 100, elapsed: -time.Second, terminal: true},
		{name: "overflow estimate", maxOffset: math.MaxUint64, timeout: 1, elapsed: time.Second, terminal: true},
		{name: "legacy wrapped timeout", maxOffset: 100, timeout: math.MaxUint64, elapsed: time.Second, terminal: true},
		{name: "negative result", maxOffset: 100, timeout: 100, model: holdoverModelFunc(func(time.Duration) (int64, error) { return -1, nil }), terminal: true},
		{name: "model error", maxOffset: 100, timeout: 100, model: holdoverModelFunc(func(time.Duration) (int64, error) { return 0, errors.New("estimate failed") }), terminal: true},
	} {
		t.Run(sample.name, func(t *testing.T) {
			options := []Option{func(d *DpllConfig) {
				d.LocalMaxHoldoverOffSet, d.LocalHoldoverTimeout = sample.maxOffset, sample.timeout
			}}
			if sample.model != nil {
				options = append(options, modelOption(t, sample.model))
			}
			dpll := newEstimatorDPLL(t, event.TBC, event.PTP4l, options...)
			if sample.timeout != 0 {
				prepareModeledEpisode(dpll)
				dpll.evaluateModeledHoldover(dpll.holdoverGeneration, dpll.holdoverStart.Add(sample.elapsed))
			} else {
				dpll.state = event.PTP_HOLDOVER
			}
			received := receiveDPLLObservation(t, dpll)
			require.Equal(t, sample.terminal, received.State == event.PTP_FREERUN)
			require.Equal(t, int64(3), dpll.phaseOffset)
		})
	}
}

func TestEstimatorHardwareEligibilityDoesNotEvaluateWhileLocked(t *testing.T) {
	for _, source := range []event.EventSource{event.GNSS, event.PTP4l, event.PPS} {
		for _, flags := range []Flag{0, FlagOnlyPhaseStatus} {
			var calls atomic.Int64
			option, err := WithHoldoverModel(holdoverModelFunc(func(time.Duration) (int64, error) { calls.Add(1); return 5, nil }))
			require.NoError(t, err)
			dpll := newEstimatorDPLL(t, event.TBC, source, option)
			dpll.flags = flags
			setModeledHardware(dpll, DPLL_LOCKED_HO_ACQ)
			require.Zero(t, calls.Load())
			if flags != 0 || source == event.PPS {
				setModeledHardware(dpll, DPLL_HOLDOVER)
				require.Zero(t, calls.Load())
				require.False(t, dpll.onHoldover)
			}
		}
	}
}

func TestSharedDefaultSelectionFromLegacyProfile(t *testing.T) {
	profile := &ptpv1.PtpProfile{PtpSettings: map[string]string{LocalMaxHoldoverOffSetStr: "100", LocalHoldoverTimeoutStr: "100", MaxInSpecOffsetStr: "40"}}
	inSpec, maximum, timeout, _, _ := CalculateTimer(profile)
	dpll := NewDpll(1, uint64(maximum), uint64(timeout), uint64(inSpec), "profile", []event.EventSource{event.PTP4l}, MOCK, nil, 0, 0, 0, config.ProcessConfig{ClockType: event.TBC}, profile)
	defer dpll.ticker.Stop()
	observation := dpll.observationData().Holdover
	require.Equal(t, uint64(timeout), observation.TimeoutSeconds)
	require.Equal(t, uint64(maximum), observation.MaxErrorNS)
	require.Equal(t, uint64(inSpec), observation.InSpecErrorNS)
	require.Equal(t, event.PTP4l, observation.Reference)
	offset, err := dpll.holdoverModel.Estimate(1500 * time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, int64(2), offset)
}
