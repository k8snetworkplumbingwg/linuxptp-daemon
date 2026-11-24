package dpll

import (
	"math"
	"testing"
	"testing/synctest"
	"time"

	nl "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/mdlayher/genetlink"
	"github.com/mdlayher/netlink"
	"github.com/stretchr/testify/require"
)

func arrivalPin(t *testing.T, offset *int64, selected bool) *nl.PinInfo {
	t.Helper()
	encoder := netlink.NewAttributeEncoder()
	encoder.Uint64(nl.DpllPinClockID, 1)
	encoder.Nested(nl.DpllPinParentDevice, func(parent *netlink.AttributeEncoder) error {
		parent.Uint32(nl.DpllPinParentID, 1)
		parent.Uint32(nl.DpllPinDirection, nl.PinDirectionInput)
		state := uint32(nl.PinStateConnected)
		if !selected {
			state = nl.PinStateDisconnected
		}
		parent.Uint32(nl.DpllPinState, state)
		if offset != nil {
			parent.Int64(nl.DpllPinPhaseOffset, *offset)
		}
		return nil
	})
	data, err := encoder.Encode()
	require.NoError(t, err)
	pins, err := nl.ParsePinReplies([]genetlink.Message{{Data: data}})
	require.NoError(t, err)
	require.Len(t, pins, 1)
	return pins[0]
}

func TestArrivalWorkerPreemptionAndTotalAge(t *testing.T) {
	for _, mode := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(mode), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var ages []time.Duration
				option, err := WithHoldoverModel(holdoverModelFunc(func(age time.Duration) (int64, error) {
					ages = append(ages, age)
					return 5, nil
				}))
				require.NoError(t, err)
				dpll := newEstimatorDPLL(t, mode, option)
				dpll.devices = []*nl.DoDeviceGetReply{{ID: 1, ClockID: 1, Type: nl.DpllTypePPS}}
				dpll.phaseStatus, dpll.frequencyStatus = DPLL_HOLDOVER, DPLL_HOLDOVER
				dpll.LocalHoldoverTimeout = 2
				dpll.Lock()
				dpll.stateDecision()
				dpll.Unlock()
				synctest.Wait()
				start := time.Now()
				for index := range 3 {
					if index == 2 {
						time.Sleep(500*time.Millisecond + time.Nanosecond)
						synctest.Wait()
						dpll.RLock()
						require.False(t, dpll.inSpec, "T must expire before the next arrival or model fallback")
						dpll.RUnlock()
						require.Empty(t, ages)
						time.Sleep(250*time.Millisecond - time.Nanosecond)
					} else {
						time.Sleep(750 * time.Millisecond)
					}
					zero := int64(0)
					dpll.applyStateUpdate(nil, []*nl.PinInfo{arrivalPin(t, &zero, true)})
					synctest.Wait()
					dpll.RLock()
					require.Zero(t, dpll.phaseOffset)
					require.Equal(t, start, dpll.holdoverStart)
					require.Equal(t, time.Now().Add(time.Second), dpll.modeledDeadline)
					dpll.RUnlock()
					require.Empty(t, ages)
				}
				dpll.RLock()
				require.False(t, dpll.inSpec, "continuous zero arrivals must not postpone T")
				dpll.RUnlock()
				time.Sleep(time.Second)
				synctest.Wait()
				require.Equal(t, []time.Duration{3250 * time.Millisecond}, ages)
				dpll.Lock()
				require.Equal(t, int64(5), dpll.phaseOffset)
				dpll.phaseStatus, dpll.frequencyStatus = DPLL_LOCKED_HO_ACQ, DPLL_LOCKED_HO_ACQ
				dpll.phaseOffset = 0
				dpll.stateDecision()
				dpll.Unlock()
				synctest.Wait()
				time.Sleep(time.Second)
				synctest.Wait()
				dpll.RLock()
				require.False(t, dpll.onHoldover)
				require.Nil(t, dpll.holdoverTimer)
				dpll.RUnlock()
			})
		})
	}
}

func TestArrivalReceiptSelectionAndDeadline(t *testing.T) {
	for _, mode := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(mode), func(t *testing.T) {
			dpll := newEstimatorDPLL(t, mode)
			dpll.devices = []*nl.DoDeviceGetReply{{ID: 1, ClockID: 1, Type: nl.DpllTypePPS}}
			dpll.phaseStatus, dpll.frequencyStatus = DPLL_HOLDOVER, DPLL_HOLDOVER
			dpll.onHoldover = true
			dpll.holdoverStart = time.Now()
			dpll.modeledDeadline = dpll.holdoverStart.Add(time.Second)
			original := dpll.modeledDeadline
			offset := int64(-41000000)
			absent := arrivalPin(t, nil, true)
			require.Equal(t, int64(math.MaxInt64), absent.ParentDevice[0].PhaseOffset)
			wrongClock := arrivalPin(t, &offset, true)
			wrongClock.ClockID = 2
			wrongParent := arrivalPin(t, &offset, true)
			wrongParent.ParentDevice[0].ParentID = 2
			for _, pin := range []*nl.PinInfo{absent, arrivalPin(t, &offset, false), wrongClock, wrongParent} {
				dpll.applyStateUpdate(nil, []*nl.PinInfo{pin})
				require.Equal(t, original, dpll.modeledDeadline)
			}
			dpll.applyStateUpdate([]*nl.DoDeviceGetReply{{ID: 1, ClockID: 1, Type: nl.DpllTypePPS, LockStatus: DPLL_HOLDOVER}}, nil)
			require.Equal(t, original, dpll.modeledDeadline)
			for range 2 {
				dpll.applyStateUpdate(nil, []*nl.PinInfo{arrivalPin(t, &offset, true)})
				require.Equal(t, int64(-41), dpll.phaseOffset)
				require.Equal(t, int64(-41), dpll.rawPhaseOffset)
				require.False(t, dpll.inSpec)
				require.True(t, dpll.modeledDeadline.After(original))
			}
			zero := int64(0)
			dpll.applyStateUpdate(nil, []*nl.PinInfo{arrivalPin(t, &zero, true), absent})
			require.Zero(t, dpll.phaseOffset, "later status-only pin must not erase a received sample")
			dpll.applyStateUpdate([]*nl.DoDeviceGetReply{{ID: 1, ClockID: 1, Type: nl.DpllTypePPS, LockStatus: DPLL_FREERUN}}, []*nl.PinInfo{arrivalPin(t, &offset, true)})
			require.Equal(t, event.PTP_FREERUN, dpll.state)
		})
	}
}

func TestArrivalNoPhaseFallbackAndTerminalRetirement(t *testing.T) {
	for _, mode := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(mode), func(t *testing.T) {
			dpll := newEstimatorDPLL(t, mode)
			dpll.flags = FlagNoPhaseOffset
			dpll.onHoldover = true
			dpll.phaseStatus, dpll.frequencyStatus = DPLL_HOLDOVER, DPLL_HOLDOVER
			dpll.holdoverStart = time.Now()
			dpll.modeledDeadline = dpll.holdoverStart.Add(time.Second)
			dpll.receiveHoldoverOffset(101, dpll.holdoverStart)
			require.Equal(t, int64(3), dpll.phaseOffset)
			require.Equal(t, dpll.holdoverStart.Add(time.Second), dpll.modeledDeadline)
			synctest.Test(t, func(t *testing.T) {
				active := newEstimatorDPLL(t, mode)
				active.phaseStatus, active.frequencyStatus = DPLL_HOLDOVER, DPLL_HOLDOVER
				active.Lock()
				active.stateDecision()
				active.Unlock()
				synctest.Wait()
				active.Lock()
				active.receiveHoldoverOffset(-101, time.Now())
				active.Unlock()
				synctest.Wait()
				active.RLock()
				require.Equal(t, event.PTP_FREERUN, active.state)
				require.False(t, active.onHoldover)
				require.Nil(t, active.holdoverTimer)
				active.RUnlock()
			})
		})
	}
}

func TestArrivalProcessedOrderAndLogFields(t *testing.T) {
	for _, mode := range []event.ClockType{event.GM, event.TBC} {
		dpll := newEstimatorDPLL(t, mode)
		dpll.onHoldover = true
		dpll.phaseStatus, dpll.frequencyStatus = DPLL_HOLDOVER, DPLL_HOLDOVER
		dpll.holdoverStart = time.Now()
		dpll.modeledDeadline = dpll.holdoverStart.Add(time.Second)
		dpll.holdoverModel = holdoverModelFunc(func(age time.Duration) (int64, error) {
			require.Equal(t, 2*time.Second, age)
			return 100, nil
		})
		dpll.receiveHoldoverOffset(-100, dpll.modeledDeadline)
		require.Equal(t, int64(-100), dpll.phaseOffset)
		require.False(t, dpll.advanceHoldoverDeadline(dpll.holdoverStart.Add(time.Second)))
		require.False(t, dpll.advanceHoldoverDeadline(dpll.holdoverStart.Add(2*time.Second)))
		dpll.receiveHoldoverOffset(-101, dpll.holdoverStart.Add(2*time.Second))
		require.Equal(t, event.PTP_FREERUN, dpll.state)
		require.False(t, dpll.inSpec)
	}
	measured := holdoverOffsetMessage("eth0", "measured", "measurement_preempted_modeled", -41, 2*time.Second)
	require.Contains(t, measured, "offset_source=measured reason=measurement_preempted_modeled offset_ns=-41 elapsed=2s modeled_fallback_after=1s")
	modeled := holdoverOffsetMessage("eth0", "modeled", "fallback_deadline", 80, 3*time.Second)
	require.Contains(t, modeled, "offset_source=modeled reason=fallback_deadline offset_ns=80 elapsed=3s")
}

func TestArrivalLargeTimeoutDoesNotWrap(t *testing.T) {
	dpll := newEstimatorDPLL(t, event.TBC)
	dpll.LocalHoldoverTimeout = math.MaxUint64
	dpll.holdoverStart = time.Now()
	dpll.modeledDeadline = dpll.holdoverStart.Add(time.Second)
	require.Equal(t, time.Second, dpll.holdoverDelay(dpll.holdoverStart))
}
