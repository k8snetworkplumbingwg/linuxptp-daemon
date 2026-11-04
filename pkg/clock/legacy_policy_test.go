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
