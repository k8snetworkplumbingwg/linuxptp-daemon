package clock

import (
	"testing"
	"time"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/stretchr/testify/require"
)

func TestAsyncGMRetainsLegacyQualityPolicy(t *testing.T) {
	for _, sample := range []struct {
		state          event.PTPState
		out, frequency bool
		class          fbprotocol.ClockClass
	}{
		{event.PTP_HOLDOVER, false, false, fbprotocol.ClockClass7},
		{event.PTP_HOLDOVER, true, true, fbprotocol.ClockClass7},
		{event.PTP_FREERUN, true, true, protocol.ClockClassOutOfSpec},
		{event.PTP_FREERUN, true, false, protocol.ClockClassFreerun},
	} {
		gm, _, client := newTestGMClock(t)
		gm.data = []*event.Data{gmData(event.GNSS, event.PTP_LOCKED), gmData(event.TS2PHC, event.PTP_LOCKED)}
		state := gm.AddEvent(event.Event{Source: event.DPLL, IFace: "ens1f0", Time: time.Now().UnixMilli(),
			Data: &event.DPLLData{State: sample.state, OutOfSpec: sample.out, FrequencyTraceable: sample.frequency, LeadingSource: true, Offset: utils.ToPtr[int64](500)}})
		require.Equal(t, sample.class, state.ClockClass)
		waitGM(t, gm)
		calls := client.SnapshotSetCalls()
		require.Len(t, calls, 1)
		settings := calls[0].GMSettings
		require.True(t, settings.TimePropertiesDS.FrequencyTraceable)
		require.Equal(t, sample.class == fbprotocol.ClockClass7, settings.TimePropertiesDS.TimeTraceable)
		if sample.class != protocol.ClockClassFreerun {
			require.Equal(t, fbprotocol.ClockAccuracyFromOffset(500*time.Nanosecond), settings.ClockQuality.ClockAccuracy)
		}
	}
}

func TestObservationMoveRetainsLegacyGNSSPolicy(t *testing.T) {
	for _, sample := range []struct {
		name                               string
		estimate                           int64
		elapsed                            time.Duration
		frequency, absent, zeroMax, failed bool
		inSpec                             uint64
		state                              event.PTPState
		class                              fbprotocol.ClockClass
	}{
		{name: "strict I equality", estimate: 40, elapsed: time.Second, inSpec: 40, state: event.PTP_HOLDOVER, class: fbprotocol.ClockClass7},
		{name: "I crossing ends holdover", estimate: 41, elapsed: time.Second, inSpec: 40, state: event.PTP_FREERUN, class: protocol.ClockClassFreerun},
		{name: "no separate F exit", estimate: 101, elapsed: time.Second, inSpec: 140, state: event.PTP_HOLDOVER, class: fbprotocol.ClockClass7},
		{name: "traceable GNSS ignores error limit", estimate: 101, elapsed: time.Second, frequency: true, inSpec: 40, state: event.PTP_HOLDOVER, class: fbprotocol.ClockClass7},
		{name: "nil skips error limits", estimate: 101, elapsed: time.Second, absent: true, inSpec: 40, state: event.PTP_HOLDOVER, class: fbprotocol.ClockClass7},
		{name: "hard timeout equality", estimate: 10, elapsed: 100 * time.Second, inSpec: 40, state: event.PTP_FREERUN, class: protocol.ClockClassFreerun},
		{name: "hard timeout nil", estimate: 10, elapsed: 100 * time.Second, absent: true, inSpec: 40, state: event.PTP_FREERUN, class: protocol.ClockClassFreerun},
		{name: "old traceable freerun class", estimate: 10, elapsed: 100 * time.Second, frequency: true, inSpec: 40, state: event.PTP_FREERUN, class: protocol.ClockClassOutOfSpec},
		{name: "legacy zero drift", zeroMax: true, elapsed: time.Second, inSpec: 40, state: event.PTP_HOLDOVER, class: fbprotocol.ClockClass7},
		{name: "model error", failed: true, elapsed: time.Second, inSpec: 40, state: event.PTP_FREERUN, class: protocol.ClockClassFreerun},
	} {
		t.Run(sample.name, func(t *testing.T) {
			gm, _, _ := newTestGMClock(t)
			gm.data = []*event.Data{gmData(event.GNSS, event.PTP_LOCKED), gmData(event.TS2PHC, event.PTP_LOCKED)}
			offset := utils.ToPtr[int64](3)
			maxError := uint64(100)
			if sample.zeroMax {
				maxError = 0
				offset = utils.ToPtr[int64](0)
			}
			if sample.absent {
				offset = nil
			}
			data := &event.DPLLData{State: event.PTP_HOLDOVER, Offset: offset, LeadingSource: true, FrequencyTraceable: sample.frequency,
				Holdover: &event.HoldoverObservation{ProducerID: 1, Sequence: 1, Episode: 1, Reference: event.GNSS,
					HardwareState: event.PTP_HOLDOVER, Active: true, Acquired: true, Valid: !sample.failed, ErrorObserved: sample.failed,
					EstimatedErrorNS: sample.estimate, PeakErrorNS: sample.estimate, Elapsed: sample.elapsed,
					TimeoutSeconds: 100, MaxErrorNS: maxError, InSpecErrorNS: sample.inSpec}}
			ev := event.Event{Source: event.DPLL, IFace: testTBCIface, Time: 1, Data: data}
			state := gm.AddEvent(ev)
			require.Equal(t, sample.state, state.State)
			require.Equal(t, sample.class, state.ClockClass)
			require.False(t, state.SuppressReport)
			require.NotNil(t, state.Report)
			reported := state.Report.Data.(*event.DPLLData)
			require.False(t, reported.ModeledHoldover)
			require.Same(t, offset, data.Offset)
			require.Equal(t, event.PTP_HOLDOVER, data.State)
			if sample.absent {
				require.Nil(t, reported.Offset)
			} else {
				expected := sample.estimate
				if sample.state == event.PTP_FREERUN {
					expected = FaultyPhaseOffset
				}
				require.Equal(t, expected, *reported.Offset)
			}
		})
	}
}
