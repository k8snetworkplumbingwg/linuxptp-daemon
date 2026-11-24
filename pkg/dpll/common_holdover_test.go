package dpll

import (
	"errors"
	"math"
	"testing"
	"time"

	nl "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/stretchr/testify/require"
)

func TestCommonModeledHoldoverBoundaries(t *testing.T) {
	for _, mode := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(mode), func(t *testing.T) {
			for _, sample := range []struct {
				name                       string
				offset                     int64
				elapsed                    time.Duration
				inSpecLimit, terminalLimit uint64
				terminal, outOfSpec        bool
			}{
				{"in-spec equality", 40, 10 * time.Second, 40, 100, false, false},
				{"in-spec crossed", 41, 10 * time.Second, 40, 100, false, true},
				{"time equality", 1, 100 * time.Second, 40, 100, false, false},
				{"timeout only", 1, 100*time.Second + time.Nanosecond, 40, 100, false, true},
				{"terminal equality", 100, 100 * time.Second, 40, 100, false, true},
				{"simultaneous terminal", 101, 101 * time.Second, 40, 100, true, true},
				{"terminal below in-spec", 21, time.Second, 40, 20, true, true},
				{"equal bounds skip degraded", 41, time.Second, 40, 40, true, true},
				{"zero terminal zero drift", 0, 1000 * time.Second, 40, 0, false, true},
			} {
				t.Run(sample.name, func(t *testing.T) {
					option, err := WithHoldoverModel(holdoverModelFunc(func(time.Duration) (int64, error) { return sample.offset, nil }))
					require.NoError(t, err)
					dpll := newEstimatorDPLL(t, mode, option)
					dpll.MaxInSpecOffset, dpll.LocalMaxHoldoverOffSet = sample.inSpecLimit, sample.terminalLimit
					dpll.phaseStatus, dpll.frequencyStatus = DPLL_HOLDOVER, DPLL_HOLDOVER
					dpll.onHoldover = true
					require.Equal(t, sample.terminal, dpll.advanceHoldover(sample.elapsed))
					require.Equal(t, !sample.outOfSpec, dpll.inSpec)
					wantState := event.PTP_HOLDOVER
					if sample.terminal {
						wantState = event.PTP_FREERUN
					}
					require.Equal(t, wantState, dpll.state)
					for range 3 {
						dpll.stateDecision()
						require.Equal(t, wantState, dpll.state)
					}
				})
			}
		})
	}
}

func TestCommonQualifiedMeasurementSelection(t *testing.T) {
	for _, mode := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(mode), func(t *testing.T) {
			for _, sample := range []struct{ model, measured int64 }{{5, 80}, {80, 5}, {5, -80}, {80, -5}} {
				dpll := newEstimatorDPLL(t, mode)
				dpll.holdoverModel = holdoverModelFunc(func(time.Duration) (int64, error) { return sample.model, nil })
				require.False(t, dpll.advanceHoldoverSample(time.Second, &sample.measured))
				require.Equal(t, sample.measured, dpll.phaseOffset, "a qualified measurement wins whether better or worse than the finite model")
				require.Equal(t, sample.measured >= -40 && sample.measured <= 40, dpll.inSpec)
			}
			for _, offset := range []int64{5, -5, 40, -40, 41, -41, 100, -100, 101, -101, math.MinInt64} {
				dpll := newEstimatorDPLL(t, mode)
				dpll.holdoverModel = holdoverModelFunc(func(time.Duration) (int64, error) {
					t.Fatal("a contract-qualified measurement must take precedence even over a failing model")
					return 0, errors.New("not reached")
				})
				terminal := offset == math.MinInt64 || offset > 100 || offset < -100
				require.Equal(t, terminal, dpll.advanceHoldoverSample(time.Second, &offset))
				require.Equal(t, offset >= -40 && offset <= 40, dpll.inSpec)
				if !terminal {
					require.Equal(t, offset, dpll.phaseOffset)
				}
			}
			for _, modelOffset := range []int64{5, 80} {
				dpll := newEstimatorDPLL(t, mode)
				dpll.holdoverModel = holdoverModelFunc(func(time.Duration) (int64, error) { return modelOffset, nil })
				require.False(t, dpll.advanceHoldover(time.Second))
				require.Equal(t, modelOffset, dpll.phaseOffset)
			}
		})
	}
}

func TestCommonScalarNetlinkArrivalAndRecovery(t *testing.T) {
	for _, mode := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(mode), func(t *testing.T) {
			dpll := newEstimatorDPLL(t, mode)
			dpll.devices = []*nl.DoDeviceGetReply{{ID: 1, ClockID: 1, Type: nl.DpllTypePPS}}
			dpll.phaseStatus, dpll.frequencyStatus = DPLL_HOLDOVER, DPLL_HOLDOVER
			dpll.onHoldover = true
			dpll.holdoverStart = time.Now()
			dpll.rawPhaseOffset = FaultyPhaseOffset
			dpll.holdoverModel = holdoverModelFunc(func(time.Duration) (int64, error) { return 50, nil })
			require.False(t, dpll.advanceHoldover(time.Second))
			require.Equal(t, int64(FaultyPhaseOffset), dpll.rawPhaseOffset, "model writes cannot become raw hardware samples")
			for _, offset := range []int64{math.MaxInt64, 0, 9000000, -9000000, 0} {
				pin := &nl.PinInfo{ClockID: 1, ParentDevice: []nl.PinParentDevice{{ParentID: 1, Direction: nl.PinDirectionInput, State: nl.PinStateConnected, PhaseOffset: offset}}}
				dpll.applyStateUpdate(nil, []*nl.PinInfo{pin})
				require.Equal(t, event.PTP_HOLDOVER, dpll.state)
				require.False(t, dpll.inSpec)
				expected := offset / 1000000
				if offset == math.MaxInt64 {
					expected = 50
				}
				require.Equal(t, expected, dpll.phaseOffset, "only received offset-bearing samples preempt the model under the arrival assumption")
				require.False(t, dpll.advanceHoldover(2*time.Second))
			}
			dpll.applyStateUpdate([]*nl.DoDeviceGetReply{{ID: 1, ClockID: 1, Type: nl.DpllTypePPS, LockStatus: DPLL_LOCKED_HO_ACQ}, {ID: 2, ClockID: 1, Type: nl.DpllTypeEEC, LockStatus: DPLL_LOCKED_HO_ACQ}}, nil)
			require.Equal(t, event.PTP_FREERUN, dpll.state, "an estimate or ambiguous reacquisition sample cannot qualify locked recovery")
			pin := &nl.PinInfo{ClockID: 1, ParentDevice: []nl.PinParentDevice{{ParentID: 1, Direction: nl.PinDirectionInput, State: nl.PinStateConnected, PhaseOffset: -5000000}}}
			dpll.applyStateUpdate(nil, []*nl.PinInfo{pin})
			require.Equal(t, event.PTP_LOCKED, dpll.state)
			require.True(t, dpll.inSpec)
			require.Equal(t, int64(-5), dpll.rawPhaseOffset)
			for _, hardware := range []uint32{DPLL_FREERUN, DPLL_HOLDOVER} {
				dpll.applyStateUpdate([]*nl.DoDeviceGetReply{{ID: 1, ClockID: 1, Type: nl.DpllTypePPS, LockStatus: hardware}}, nil)
				require.Equal(t, event.PTP_FREERUN, dpll.state, "unlocked hardware revokes admission; later HOLDOVER alone cannot reacquire")
			}
		})
	}
}

func TestCommonModeledInvalidAndDisabled(t *testing.T) {
	for _, mode := range []event.ClockType{event.GM, event.TBC} {
		for _, model := range []HoldoverModel{
			holdoverModelFunc(func(time.Duration) (int64, error) { return -1, nil }),
			holdoverModelFunc(func(time.Duration) (int64, error) { return 1, errors.New("invalid") }),
		} {
			dpll := newEstimatorDPLL(t, mode)
			dpll.holdoverModel = model
			require.True(t, dpll.advanceHoldover(time.Second))
			require.Equal(t, event.PTP_FREERUN, dpll.state)
		}
		dpll := newEstimatorDPLL(t, mode)
		dpll.LocalHoldoverTimeout = 0
		dpll.holdoverModel = holdoverModelFunc(func(time.Duration) (int64, error) { t.Fatal("disabled before estimate"); return 0, nil })
		require.True(t, dpll.advanceHoldover(time.Second))
		require.Equal(t, event.PTP_FREERUN, dpll.state)
	}
}

func TestCommonHardwareAuthority(t *testing.T) {
	for _, mode := range []event.ClockType{event.GM, event.TBC} {
		for _, status := range []int64{DPLL_FREERUN, DPLL_INVALID, DPLL_UNKNOWN} {
			dpll := newEstimatorDPLL(t, mode)
			dpll.phaseStatus, dpll.frequencyStatus = status, status
			dpll.onHoldover = true
			dpll.stateDecision()
			require.Equal(t, event.PTP_FREERUN, dpll.state)
			require.False(t, dpll.inSpec)
			require.True(t, dpll.sourceLost)
		}
		dpll := newEstimatorDPLL(t, mode)
		dpll.phaseStatus, dpll.frequencyStatus = DPLL_HOLDOVER, DPLL_FREERUN
		dpll.onHoldover = true
		dpll.stateDecision()
		if mode == event.GM {
			require.Equal(t, event.PTP_FREERUN, dpll.state)
		} else {
			require.Equal(t, event.PTP_HOLDOVER, dpll.state)
		}
	}
}
