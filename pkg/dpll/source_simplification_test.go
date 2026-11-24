package dpll

import (
	"fmt"
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/stretchr/testify/require"
)

func TestLeadingSourceClassification(t *testing.T) {
	for _, sample := range []struct {
		source  event.EventSource
		leading bool
	}{
		{event.GNSS, true},
		{event.PTP4l, true},
		{event.PPS, false},
		{event.DPLL, false},
		{event.EventSource("unknown"), false},
	} {
		t.Run(string(sample.source), func(t *testing.T) {
			dpll := newEstimatorDPLL(t, event.GM)
			dpll.dependsOn = []event.EventSource{sample.source}
			events := make(chan event.Event, 64)
			dpll.processConfig.EventChannel = events
			require.Equal(t, sample.leading, dpll.hasLeadingSource())
			dpll.sendDpllEvent()
			data := (<-events).Data.(*event.DPLLData)
			require.Equal(t, sample.leading, data.LeadingSource)
		})
	}
}

func TestNoPhaseLegacySampleEquivalence(t *testing.T) {
	for _, source := range []event.EventSource{event.GNSS, event.PTP4l} {
		for _, inSpec := range []bool{false, true} {
			for _, traceable := range []bool{false, true} {
				name := fmt.Sprintf("%s/inSpec=%t/traceable=%t", source, inSpec, traceable)
				t.Run(name, func(t *testing.T) {
					option, err := WithHoldoverModel(holdoverModelFunc(func(time.Duration) (int64, error) {
						return 1000, nil
					}))
					require.NoError(t, err)
					dpll := newEstimatorDPLL(t, event.GM, option)
					events := make(chan event.Event, 64)
					dpll.processConfig.EventChannel = events
					dpll.dependsOn = []event.EventSource{source}
					dpll.flags = FlagNoPhaseOffset
					dpll.inSpec = inSpec
					dpll.frequencyTraceable = traceable
					dpll.sourceLost = true
					dpll.onHoldover = true
					dpll.phaseStatus, dpll.frequencyStatus = DPLL_HOLDOVER, DPLL_HOLDOVER
					for _, elapsed := range []time.Duration{time.Second, 100 * time.Second, 101 * time.Second} {
						require.False(t, dpll.advanceHoldover(elapsed))
						require.Equal(t, event.PTP_HOLDOVER, dpll.state)
						require.Equal(t, inSpec, dpll.inSpec)
						require.Equal(t, int64(1000), dpll.phaseOffset)
						require.True(t, dpll.onHoldover)
						require.True(t, dpll.sourceLost)
						data := (<-events).Data.(*event.DPLLData)
						require.Equal(t, event.PTP_HOLDOVER, data.State)
						require.Equal(t, !inSpec, data.OutOfSpec)
						require.Equal(t, traceable, data.FrequencyTraceable)
						require.True(t, data.LeadingSource)
						require.Nil(t, data.Offset)
					}
				})
			}
		}
	}
}

func TestNoPhaseLegacyWorkerExpiry(t *testing.T) {
	for _, mode := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(mode), func(t *testing.T) {
			dpll := newEstimatorDPLL(t, mode)
			events := make(chan event.Event, 64)
			dpll.processConfig.EventChannel = events
			dpll.flags = FlagNoPhaseOffset
			dpll.onHoldover = true
			dpll.LocalHoldoverTimeout = 0
			dpll.holdoverCloseCh = make(chan bool)
			dpll.holdover()
			require.Equal(t, event.PTP_FREERUN, dpll.state)
			require.Equal(t, int64(FaultyPhaseOffset), dpll.phaseOffset)
			require.False(t, dpll.inSpec)
			require.False(t, dpll.onHoldover)
			var last *event.DPLLData
			for len(events) > 0 {
				last = (<-events).Data.(*event.DPLLData)
			}
			require.NotNil(t, last)
			require.Equal(t, event.PTP_FREERUN, last.State)
			require.True(t, last.OutOfSpec)
			require.Nil(t, last.Offset)
		})
	}
}

func TestNoPhaseLegacyRepeatedHardware(t *testing.T) {
	for _, source := range []event.EventSource{event.GNSS, event.PTP4l} {
		for _, offset := range []int64{5, 101} {
			t.Run(fmt.Sprintf("%s/offset=%d", source, offset), func(t *testing.T) {
				dpll := newEstimatorDPLL(t, event.GM)
				events := make(chan event.Event, 64)
				dpll.processConfig.EventChannel = events
				dpll.dependsOn = []event.EventSource{source}
				dpll.flags = FlagNoPhaseOffset
				dpll.phaseOffset = offset
				dpll.onHoldover = true
				dpll.phaseStatus, dpll.frequencyStatus = DPLL_HOLDOVER, DPLL_HOLDOVER
				expected := event.PTP_HOLDOVER
				if source == event.PTP4l && offset > int64(dpll.LocalMaxHoldoverOffSet) {
					expected = event.PTP_FREERUN
				}
				for range 3 {
					dpll.stateDecision()
					require.Equal(t, expected, dpll.state)
					data := (<-events).Data.(*event.DPLLData)
					require.Equal(t, expected, data.State)
					if expected == event.PTP_FREERUN {
						require.Equal(t, int64(FaultyPhaseOffset), dpll.phaseOffset)
						require.True(t, data.SourceLost)
					}
					require.Nil(t, data.Offset)
				}
			})
		}
	}
}

func TestSourceHardwareStatusSelection(t *testing.T) {
	for _, source := range []event.EventSource{event.GNSS, event.PTP4l, event.PPS} {
		for _, flags := range []Flag{0, FlagNoPhaseOffset, FlagNoPhaseStatus, FlagNoFreqencyStatus, FlagNoPhaseOffset | FlagNoPhaseStatus, FlagNoPhaseOffset | FlagNoFreqencyStatus} {
			t.Run(fmt.Sprintf("%s/flags=%d", source, flags), func(t *testing.T) {
				dpll := newEstimatorDPLL(t, event.GM)
				dpll.dependsOn = []event.EventSource{source}
				dpll.flags = flags
				dpll.phaseStatus, dpll.frequencyStatus = DPLL_HOLDOVER, DPLL_FREERUN
				expected := int64(DPLL_FREERUN)
				if source == event.PTP4l || source == event.PPS || flags&FlagNoFreqencyStatus != 0 {
					expected = DPLL_HOLDOVER
				}
				require.Equal(t, expected, dpll.getDpllState())
			})
		}
	}
}
