package clock

import (
	"fmt"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/stretchr/testify/require"
)

func TestDPLLPreparationViewsAndStoredFields(t *testing.T) {
	for _, gm := range []bool{false, true} {
		for _, absent := range []bool{false, true} {
			for _, state := range []event.PTPState{event.PTP_LOCKED, event.PTP_HOLDOVER, event.PTP_FREERUN} {
				t.Run(fmt.Sprintf("gm=%t/absent=%t/%s", gm, absent, state), func(t *testing.T) {
					base := BaseClock{osClock: &OsClock{}}
					ev := rawHoldoverSample()
					raw := ev.Data.(*event.DPLLData)
					raw.Offset = utils.ToPtr[int64](-3)
					if absent {
						raw.Offset = nil
					}
					raw.Holdover.HardwareState = state
					raw.Holdover.EstimatedErrorNS, raw.Holdover.PeakErrorNS = 25, 25
					original := *raw
					policy, result, accepted := base.prepareDPLL(ev, gm)
					require.True(t, accepted)
					require.False(t, result.SuppressReport)
					require.NotNil(t, result.Report)
					view := result.Report.Data.(*event.DPLLData)
					data := base.GetData(event.DPLL)
					data.AddEvent(policy)
					data.UpdateState()
					detail := data.GetDataDetails(ev.IFace)
					policyData := policy.Data.(*event.DPLLData)
					require.Equal(t, policyData.State, detail.State)
					require.Equal(t, policyData.OutOfSpec, detail.OutOfSpec)
					require.Equal(t, policyData.SourceLost, detail.SourceLost)
					require.Equal(t, policyData.ModeledHoldover, detail.ModeledHoldover)
					require.Equal(t, policyData.Offset != nil, detail.HasOffset)
					require.Equal(t, detail.State, view.State)
					require.Equal(t, detail.OutOfSpec, view.OutOfSpec)
					require.Equal(t, detail.SourceLost, view.SourceLost)
					require.Equal(t, detail.ModeledHoldover, view.ModeledHoldover)
					require.Same(t, raw.Holdover, view.Holdover)
					require.Equal(t, original, *raw)
					if absent {
						require.Nil(t, view.Offset)
					} else {
						require.Equal(t, *policyData.Offset, detail.Offset)
						expected := int64(-3)
						switch state {
						case event.PTP_HOLDOVER:
							expected = 25
						case event.PTP_FREERUN:
							expected = FaultyPhaseOffset
						}
						require.Equal(t, expected, *view.Offset)
						stored := detail.Offset
						*view.Offset = 999
						require.Equal(t, stored, detail.Offset)
						require.Equal(t, int64(-3), *raw.Offset)
					}
				})
			}
		}
	}
}

func TestDPLLPreparationRetainsDuplicatesAndRejectsResetEpisode(t *testing.T) {
	base := BaseClock{osClock: &OsClock{}}
	ev := rawHoldoverSample()
	policy, result, accepted := base.prepareDPLL(ev, true)
	require.True(t, accepted)
	base.GetData(event.DPLL).AddEvent(policy)
	want := *result.Report.Data.(*event.DPLLData).Offset
	duplicate := *ev.Data.(*event.DPLLData)
	observation := *duplicate.Holdover
	observation.EstimatedErrorNS = 999
	duplicate.Holdover = &observation
	ev.Data = &duplicate
	_, result, accepted = base.prepareDPLL(ev, true)
	require.False(t, accepted)
	require.False(t, result.SuppressReport)
	require.Equal(t, want, *result.Report.Data.(*event.DPLLData).Offset)
	observation.Sequence = 0
	_, result, accepted = base.prepareDPLL(ev, true)
	require.False(t, accepted)
	require.True(t, result.SuppressReport)
	observation.ProducerID = 0
	observation.Sequence = 100
	_, result, accepted = base.prepareDPLL(ev, true)
	require.False(t, accepted)
	require.True(t, result.SuppressReport)
	base.Reset()
	observation.ProducerID, observation.Sequence = 1, 2
	_, result, accepted = base.prepareDPLL(ev, true)
	require.False(t, accepted)
	require.True(t, result.SuppressReport)
	observation.Episode++
	observation.HardwareState = event.PTP_LOCKED
	_, result, accepted = base.prepareDPLL(ev, true)
	require.True(t, accepted)
	require.False(t, result.SuppressReport)
	require.Equal(t, event.PTP_LOCKED, result.Report.Data.(*event.DPLLData).State)
}

func TestTBCPreparesOnlyDPLLSource(t *testing.T) {
	bc := newPMCTestTBCClock(&pmc.MockClient{})
	ev := rawHoldoverSample()
	ev.Source = event.TS2PHC
	result := bc.AddEvent(ev)
	require.Nil(t, bc.holdover)
	require.Nil(t, result.Report)
	require.False(t, result.SuppressReport)
	ev.Source = event.DPLL
	result = bc.AddEvent(ev)
	require.NotEmpty(t, bc.holdover)
	require.NotNil(t, result.Report)
	ev.Source = event.TS2PHC
	result = bc.AddEvent(ev)
	require.NotNil(t, result.Report)
	require.False(t, result.SuppressReport)
	require.Equal(t, uint64(1), bc.holdover[ev.IFace].sequence)
}
