package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	parserconstants "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

func i64(v int64) *int64 { return &v }

func TestConfigureOSClockFilter_Defaults(t *testing.T) {
	p := &ptpProcess{}
	p.configureOSClockFilter(nil, 100)

	assert.Equal(t, 100.0, p.osClockFilter.InSyncThreshold())
	assert.Equal(t, 100.0, p.osClockFilter.OutOfSyncThreshold())
	assert.Equal(t, defaultSysOffsetSamples, p.osClockFilter.SamplesRequired())
	assert.Equal(t, event.PTP_NOTSET, osClockStateFromHysteresis(p.osClockFilter.State()))
}

func TestConfigureOSClockFilter_CustomFields(t *testing.T) {
	p := &ptpProcess{}
	p.configureOSClockFilter(&ptpv1.PtpClockThreshold{
		SysOffsetInSyncThreshold:    i64(50),
		SysOffsetOutOfSyncThreshold: i64(200),
		SysOffsetSamples:            i64(5),
	}, 100)

	assert.Equal(t, 50.0, p.osClockFilter.InSyncThreshold())
	assert.Equal(t, 200.0, p.osClockFilter.OutOfSyncThreshold())
	assert.Equal(t, 5, p.osClockFilter.SamplesRequired())
}

func TestConfigureOSClockFilter_NilFieldsUseDefaults(t *testing.T) {
	p := &ptpProcess{}
	p.configureOSClockFilter(&ptpv1.PtpClockThreshold{}, 100)

	assert.Equal(t, 100.0, p.osClockFilter.InSyncThreshold())
	assert.Equal(t, 100.0, p.osClockFilter.OutOfSyncThreshold())
	assert.Equal(t, defaultSysOffsetSamples, p.osClockFilter.SamplesRequired())
}

func TestConfigureOSClockFilter_InvalidSamplesUseDefault(t *testing.T) {
	p := &ptpProcess{}
	p.configureOSClockFilter(&ptpv1.PtpClockThreshold{
		SysOffsetSamples: i64(0),
	}, 100)

	assert.Equal(t, 100.0, p.osClockFilter.InSyncThreshold())
	assert.Equal(t, 100.0, p.osClockFilter.OutOfSyncThreshold())
	assert.Equal(t, defaultSysOffsetSamples, p.osClockFilter.SamplesRequired())
}

// TestUpdateOSClockState_Hysteresis verifies the filter only flips state after
// samplesRequired consecutive samples breach a threshold, and that jitter within
// the threshold band leaves the state unchanged.
func TestUpdateOSClockState_Hysteresis(t *testing.T) {
	p := &ptpProcess{}
	p.configureOSClockFilter(&ptpv1.PtpClockThreshold{
		SysOffsetInSyncThreshold:    i64(50),
		SysOffsetOutOfSyncThreshold: i64(100),
		SysOffsetSamples:            i64(3),
	}, 100)

	// NOTSET -> LOCKED requires 3 consecutive in-sync (abs(offset) <= 50) samples.
	_, changed := p.updateOSClockState(10, event.PTP_LOCKED)
	assert.False(t, changed, "no transition on first in-sync sample")
	_, changed = p.updateOSClockState(20, event.PTP_LOCKED)
	assert.False(t, changed, "no transition on second in-sync sample")
	state, changed := p.updateOSClockState(30, event.PTP_LOCKED)
	assert.True(t, changed, "third consecutive in-sync sample transitions to LOCKED")
	assert.Equal(t, event.PTP_LOCKED, state)

	// A single jitter sample outside the out-of-sync band must NOT flip to FREERUN.
	_, changed = p.updateOSClockState(120, event.PTP_LOCKED)
	assert.False(t, changed, "single out-of-sync sample must not flip LOCKED")
	// A single in-sync sample also leaves state unchanged.
	_, changed = p.updateOSClockState(40, event.PTP_LOCKED)
	assert.False(t, changed, "state remains LOCKED after in-sync jitter")

	// 3 consecutive out-of-sync (abs(offset) > 100) samples -> FREERUN.
	_, changed = p.updateOSClockState(150, event.PTP_LOCKED)
	assert.False(t, changed)
	_, changed = p.updateOSClockState(160, event.PTP_LOCKED)
	assert.False(t, changed)
	state, changed = p.updateOSClockState(170, event.PTP_LOCKED)
	assert.True(t, changed, "third consecutive out-of-sync sample transitions to FREERUN")
	assert.Equal(t, event.PTP_FREERUN, state)

	// 3 consecutive samples in the hysteresis band reset both counters (no transition).
	// offset 75 is within (50, 100]: resets counts.
	_, changed = p.updateOSClockState(75, event.PTP_FREERUN)
	assert.False(t, changed, "band sample should not flip FREERUN")
	_, changed = p.updateOSClockState(75, event.PTP_FREERUN)
	assert.False(t, changed)
	_, changed = p.updateOSClockState(75, event.PTP_FREERUN)
	assert.False(t, changed, "band samples reset counters, never transition")

	// Back to 3 consecutive in-sync -> LOCKED again.
	_, changed = p.updateOSClockState(10, event.PTP_FREERUN)
	assert.False(t, changed)
	_, changed = p.updateOSClockState(10, event.PTP_FREERUN)
	assert.False(t, changed)
	state, changed = p.updateOSClockState(10, event.PTP_FREERUN)
	assert.True(t, changed)
	assert.Equal(t, event.PTP_LOCKED, state)
}

// TestUpdateOSClockState_HoldoverPassthrough verifies a HOLDOVER raw state is
// forwarded as-is and never replaced by an offset-derived LOCKED/FREERUN.
func TestUpdateOSClockState_HoldoverPassthrough(t *testing.T) {
	p := &ptpProcess{}
	p.configureOSClockFilter(nil, 100)

	state, changed := p.updateOSClockState(10, event.PTP_HOLDOVER)
	assert.True(t, changed)
	assert.Equal(t, event.PTP_HOLDOVER, state)

	// Even with an in-sync offset, HOLDOVER is preserved.
	state, changed = p.updateOSClockState(5, event.PTP_HOLDOVER)
	assert.False(t, changed, "HOLDOVER is sticky until raw state leaves HOLDOVER")
	assert.Equal(t, event.PTP_HOLDOVER, state)
}

// TestProcessParsedMetrics_Phc2sysEmitsOnTransitionOnly verifies the phc2sys
// PHC2SYS event (which drives O-RAN E3) is emitted only when the filtered OS-clock
// state transitions, and reflects the filtered state rather than the raw offset.
func TestProcessParsedMetrics_Phc2sysEmitsOnTransitionOnly(t *testing.T) {
	p := &ptpProcess{
		name: phc2sysProcessName,
		ptpClockThreshold: &ptpv1.PtpClockThreshold{
			MaxOffsetThreshold: 100,
		},
		eventCh: make(chan event.Event, 10),
	}
	p.configureOSClockFilter(&ptpv1.PtpClockThreshold{
		SysOffsetInSyncThreshold:    i64(50),
		SysOffsetOutOfSyncThreshold: i64(100),
		SysOffsetSamples:            i64(2),
	}, 100)

	sample := func(offset float64, clockState parserconstants.ClockState) {
		processParsedMetrics(p, &parser.Metrics{
			Iface:      "CLOCK_REALTIME",
			Offset:     offset,
			MaxOffset:  offset,
			FreqAdj:    0,
			Delay:      0,
			ClockState: clockState,
		})
	}

	readState := func() (event.PTPState, bool) {
		select {
		case ev := <-p.eventCh:
			data, _ := ev.Data.(*event.PTPData)
			return data.State, true
		default:
			return event.PTP_NOTSET, false
		}
	}

	// No servo ClockState for the raw state: filter still derives LOCKED/FREERUN from offset.
	sample(10, parserconstants.ClockState(""))
	_, emitted := readState()
	assert.False(t, emitted, "first in-sync sample should not emit (needs 2 consecutive)")

	sample(20, parserconstants.ClockState(""))
	state, emitted := readState()
	assert.True(t, emitted, "second consecutive in-sync sample emits LOCKED")
	assert.Equal(t, event.PTP_LOCKED, state)

	// Remaining in-sync samples: no re-emit.
	sample(30, parserconstants.ClockState(""))
	_, emitted = readState()
	assert.False(t, emitted, "steady LOCKED must not re-emit")

	// One out-of-sync sample: still no transition (needs 2 consecutive).
	sample(150, parserconstants.ClockState(""))
	_, emitted = readState()
	assert.False(t, emitted, "single out-of-sync sample must not emit FREERUN")

	// Second consecutive out-of-sync sample emits FREERUN.
	sample(160, parserconstants.ClockState(""))
	state, emitted = readState()
	assert.True(t, emitted, "second consecutive out-of-sync sample emits FREERUN")
	assert.Equal(t, event.PTP_FREERUN, state)

	// Holdover passthrough emits a HOLDOVER transition.
	sample(10, parserconstants.ClockStateHoldover)
	state, emitted = readState()
	assert.True(t, emitted, "HOLDOVER raw state emits HOLDOVER")
	assert.Equal(t, event.PTP_HOLDOVER, state)
}
