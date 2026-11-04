package clock

import (
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/stretchr/testify/require"
)

func rawHoldoverSample() event.Event {
	return event.Event{Source: event.DPLL, IFace: "leading", Time: 1, Data: &event.DPLLData{
		State: event.PTP_HOLDOVER, Offset: utils.ToPtr[int64](3), LeadingSource: true, FrequencyTraceable: true,
		Holdover: &event.HoldoverObservation{ProducerID: 1, Sequence: 1, Episode: 1,
			HardwareState: event.PTP_HOLDOVER, Reference: event.GNSS, Acquired: true, Active: true,
			TimeoutSeconds: 100, MaxErrorNS: 100, InSpecErrorNS: 40, Valid: true},
	}}
}

func TestReceivingClockRejectsUnqualifiedAcquisitionAndMissedTerminalPeak(t *testing.T) {
	for _, gm := range []bool{false, true} {
		for _, unqualified := range []bool{false, true} {
			base := BaseClock{}
			ev := rawHoldoverSample()
			observation := ev.Data.(*event.DPLLData).Holdover
			if unqualified {
				observation.AcquisitionOffset = utils.ToPtr[int64](101)
			} else {
				observation.EstimatedErrorNS, observation.PeakErrorNS = 10, 101
			}
			interpreted, accepted := base.interpretDPLL(ev, gm)
			require.True(t, accepted)
			require.Equal(t, event.PTP_FREERUN, interpreted.Data.(*event.DPLLData).State)
		}
	}
}

func TestLegacyHardwareHoldoverAfterOutOfSpecRetainsExitPolicy(t *testing.T) {
	for _, gm := range []bool{false, true} {
		base := BaseClock{}
		ev := rawHoldoverSample()
		data := ev.Data.(*event.DPLLData)
		if !gm {
			data.Holdover.Reference = event.PTP4l
		}
		data.Holdover.EstimatedErrorNS, data.Holdover.PeakErrorNS = 50, 50
		interpreted, accepted := base.interpretDPLL(ev, gm)
		require.True(t, accepted)
		require.Equal(t, event.PTP_HOLDOVER, interpreted.Data.(*event.DPLLData).State)
		require.True(t, interpreted.Data.(*event.DPLLData).OutOfSpec)
		data.Holdover.Sequence++
		data.Holdover.HardwareSample = true
		interpreted, accepted = base.interpretDPLL(ev, gm)
		require.True(t, accepted)
		expected := event.PTP_FREERUN
		if gm {
			expected = event.PTP_HOLDOVER
		}
		require.Equal(t, expected, interpreted.Data.(*event.DPLLData).State)
	}
}

func TestReceivingClockResetRejectsQueuedEpisodeUntilRecovery(t *testing.T) {
	gm, err := NewGM("ts2phc.0.config", func() int { return 37 }, &pmc.MockClient{})
	require.NoError(t, err)
	gm.SetOsClock(&OsClock{})
	t.Cleanup(func() { <-gm.CancelPMC() })
	ev := rawHoldoverSample()
	_, accepted := gm.interpretDPLL(ev, true)
	require.True(t, accepted)
	gm.Reset()
	observation := ev.Data.(*event.DPLLData).Holdover
	observation.Sequence++
	_, accepted = gm.interpretDPLL(ev, true)
	require.False(t, accepted)
	observation.Sequence++
	observation.Episode++
	observation.HardwareState = event.PTP_LOCKED
	_, accepted = gm.interpretDPLL(ev, true)
	require.True(t, accepted)
}

func TestTBCObservationClassLatchTimeoutAndNilReporting(t *testing.T) {
	for _, absent := range []bool{false, true} {
		bc := newPMCTestTBCClock(&pmc.MockClient{})
		bc.SetConfiguredLeadingInterface("leading")
		bc.syncState.State = event.PTP_LOCKED
		bc.syncState.ClockClass = 6
		bc.leadingClockData.toFreeRunThreshold = 100
		bc.leadingClockData.MaxInSpecOffset = 40
		bc.leadingClockData.inSyncConditionThreshold = 10
		bc.leadingClockData.inSyncConditionTimes = 1
		bc.leadingClockData.initInSyncFilter()
		for index, sample := range []struct {
			estimate, peak int64
			elapsed        time.Duration
			class          uint8
		}{
			{10, 10, time.Second, 135}, {50, 50, 2 * time.Second, 165}, {10, 50, 3 * time.Second, 165}, {10, 50, 100 * time.Second, 248},
		} {
			ev := rawHoldoverSample()
			ev.Time = time.Now().UnixMilli()
			data := ev.Data.(*event.DPLLData)
			data.Holdover.Reference = event.PTP4l
			data.Holdover.Sequence = uint64(index + 1)
			data.Holdover.EstimatedErrorNS, data.Holdover.PeakErrorNS = sample.estimate, sample.peak
			data.Holdover.Elapsed = sample.elapsed
			if absent {
				data.Offset = nil
			}
			state := bc.AddEvent(ev)
			expected := sample.class
			if absent && expected == 165 {
				expected = 135
			}
			require.Equal(t, expected, uint8(state.ClockClass))
			detail := bc.GetData(event.DPLL).GetDataDetails("leading")
			require.Equal(t, !absent, detail.HasOffset)
			require.Equal(t, data.Holdover, detail.Holdover)
			require.False(t, data.OutOfSpec)
			if absent {
				require.Nil(t, data.Offset)
			} else {
				require.Equal(t, int64(3), *data.Offset)
			}
			if expected == uint8(protocol.ClockClassFreerun) {
				require.Equal(t, event.PTP_FREERUN, state.State)
			}
		}
	}
}

func TestReceivingClockOffsetQualification(t *testing.T) {
	for _, sample := range []struct {
		offset int64
		bound  uint64
		within bool
	}{
		{50, 14400, true}, {-50, 14400, true}, {15000, 14400, false}, {-15000, 14400, false},
		{14400, 14400, true}, {-14400, 14400, true}, {0, 0, true}, {2000, 14400, true}, {2000, 1500, false},
	} {
		for _, absent := range []bool{false, true} {
			base := BaseClock{}
			ev := rawHoldoverSample()
			data := ev.Data.(*event.DPLLData)
			data.State, data.Holdover.HardwareState = event.PTP_LOCKED, event.PTP_LOCKED
			data.Holdover.Reference, data.Holdover.MaxErrorNS = event.PTP4l, sample.bound
			data.Offset = utils.ToPtr(sample.offset)
			if absent {
				data.Offset = nil
			}
			result, accepted := base.interpretDPLL(ev, false)
			require.True(t, accepted)
			require.Equal(t, sample.within || absent, result.Data.(*event.DPLLData).State == event.PTP_LOCKED)
		}
	}
}

func TestSameObservationDistinctClockTimeoutAndOffsetPolicy(t *testing.T) {
	for _, elapsed := range []time.Duration{99 * time.Second, 100 * time.Second, 101 * time.Second} {
		ev := rawHoldoverSample()
		data := ev.Data.(*event.DPLLData)
		data.Holdover.Elapsed, data.Holdover.EstimatedErrorNS, data.Holdover.PeakErrorNS = elapsed, 50, 50
		gm, bc := BaseClock{}, BaseClock{}
		gmEvent, ok := gm.interpretDPLL(ev, true)
		require.True(t, ok)
		bcEvent, ok := bc.interpretDPLL(ev, false)
		require.True(t, ok)
		require.Equal(t, event.PTP_HOLDOVER, gmEvent.Data.(*event.DPLLData).State)
		require.True(t, gmEvent.Data.(*event.DPLLData).OutOfSpec)
		require.Equal(t, int64(3), *gmEvent.Data.(*event.DPLLData).Offset)
		if elapsed >= 100*time.Second {
			require.Equal(t, event.PTP_FREERUN, bcEvent.Data.(*event.DPLLData).State)
		} else {
			require.Equal(t, event.PTP_HOLDOVER, bcEvent.Data.(*event.DPLLData).State)
			require.Equal(t, int64(50), *bcEvent.Data.(*event.DPLLData).Offset)
		}
		require.Equal(t, event.PTP_HOLDOVER, data.State)
		require.False(t, data.OutOfSpec)
		require.Equal(t, int64(3), *data.Offset)
	}
}

func TestClockLatchesFromCumulativeFactsAndRejectsReordering(t *testing.T) {
	base := BaseClock{}
	ev := rawHoldoverSample()
	data := ev.Data.(*event.DPLLData)
	data.Holdover.PeakErrorNS, data.Holdover.EstimatedErrorNS = 50, 10
	interpreted, ok := base.interpretDPLL(ev, true)
	require.True(t, ok)
	require.True(t, interpreted.Data.(*event.DPLLData).OutOfSpec)
	data.Holdover.Sequence = 0
	_, ok = base.interpretDPLL(ev, true)
	require.False(t, ok)
	data.Holdover.Sequence, data.Holdover.ModelRevision, data.Holdover.PeakErrorNS = 2, 2, 10
	interpreted, ok = base.interpretDPLL(ev, true)
	require.True(t, ok)
	require.True(t, interpreted.Data.(*event.DPLLData).OutOfSpec)
	data.Holdover.Sequence, data.Holdover.PeakErrorNS = 3, 101
	interpreted, ok = base.interpretDPLL(ev, true)
	require.True(t, ok)
	require.Equal(t, event.PTP_FREERUN, interpreted.Data.(*event.DPLLData).State)
	data.Holdover.Sequence, data.Holdover.PeakErrorNS = 4, 1
	interpreted, _ = base.interpretDPLL(ev, true)
	require.Equal(t, event.PTP_FREERUN, interpreted.Data.(*event.DPLLData).State)
	data.Holdover.Sequence, data.Holdover.Episode = 5, 2
	interpreted, _ = base.interpretDPLL(ev, true)
	require.Equal(t, event.PTP_HOLDOVER, interpreted.Data.(*event.DPLLData).State)
	require.False(t, interpreted.Data.(*event.DPLLData).OutOfSpec)
	data.Holdover.ProducerID, data.Holdover.Sequence = 2, 1
	_, ok = base.interpretDPLL(ev, true)
	require.True(t, ok)
	data.Holdover.ProducerID, data.Holdover.Sequence = 1, 100
	_, ok = base.interpretDPLL(ev, true)
	require.False(t, ok)
}
