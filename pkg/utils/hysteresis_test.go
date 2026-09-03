package utils

import (
	"testing"
)

func TestHysteresisFilterConfig(t *testing.T) {
	f := NewHysteresisFilter(50, 100, 2)
	if f.InSyncThreshold() != 50 {
		t.Fatalf("inSyncThreshold=%v, want 50", f.InSyncThreshold())
	}
	if f.OutOfSyncThreshold() != 100 {
		t.Fatalf("outOfSyncThreshold=%v, want 100", f.OutOfSyncThreshold())
	}
	if f.SamplesRequired() != 2 {
		t.Fatalf("samplesRequired=%d, want 2", f.SamplesRequired())
	}
	if f.State() != HysteresisUnknown {
		t.Fatalf("state=%v, want Unknown", f.State())
	}
	f.Config(60, 120, 3)
	if f.InSyncThreshold() != 60 || f.OutOfSyncThreshold() != 120 || f.SamplesRequired() != 3 {
		t.Fatalf("Config did not update: in=%v out=%v samples=%d",
			f.InSyncThreshold(), f.OutOfSyncThreshold(), f.SamplesRequired())
	}
}

func TestHysteresisFilterLowAfterConsecutive(t *testing.T) {
	f := NewHysteresisFilter(50, 100, 2)
	state, _ := f.Update(25)
	if state != HysteresisUnknown {
		t.Fatalf("first sample state=%v, want Unknown (needs 2 consecutive)", state)
	}
	state, _ = f.Update(25)
	if state != HysteresisLow {
		t.Fatalf("second sample state=%v, want Low", state)
	}
	state, _ = f.Update(25)
	if state != HysteresisLow {
		t.Fatalf("third sample state=%v, want Low (stable)", state)
	}
}

func TestHysteresisFilterHighAfterConsecutive(t *testing.T) {
	f := NewHysteresisFilter(50, 100, 2)
	state, _ := f.Update(150)
	if state != HysteresisUnknown {
		t.Fatalf("first sample state=%v, want Unknown (needs 2 consecutive)", state)
	}
	state, _ = f.Update(150)
	if state != HysteresisHigh {
		t.Fatalf("second sample state=%v, want High", state)
	}
}

func TestHysteresisFilterBandResetsCounters(t *testing.T) {
	f := NewHysteresisFilter(50, 100, 3)
	_, changed := f.Update(150)
	if changed {
		t.Fatalf("unexpected transition on first sample")
	}
	// A band sample resets the out-of-sync counter.
	f.Update(75)
	// If the counter were not reset, two more out-of-sync samples (1+2=3) would
	// suffice; because it was reset we need the full three again.
	f.Update(150)
	f.Update(150)
	state, changed := f.Update(150)
	if state != HysteresisHigh || !changed {
		t.Fatalf("state=%v changed=%v, want High only after full run post-band-reset", state, changed)
	}
}

func TestHysteresisFilterTransitionChangedFlag(t *testing.T) {
	f := NewHysteresisFilter(50, 100, 1)
	for _, tc := range []struct {
		value   float64
		want    HysteresisState
		wantChg bool
	}{
		{25, HysteresisLow, true},   // Unknown -> Low
		{25, HysteresisLow, false},  // stable
		{150, HysteresisHigh, true}, // Low -> High
		{150, HysteresisHigh, false},
	} {
		state, changed := f.Update(tc.value)
		if state != tc.want || changed != tc.wantChg {
			t.Fatalf("value=%v: state=%v changed=%v, want state=%v changed=%v",
				tc.value, state, changed, tc.want, tc.wantChg)
		}
	}
}

func TestHysteresisFilterReset(t *testing.T) {
	f := NewHysteresisFilter(50, 100, 1)
	f.Update(25)
	if f.State() != HysteresisLow {
		t.Fatalf("state=%v, want Low before reset", f.State())
	}
	f.Reset()
	if f.State() != HysteresisUnknown {
		t.Fatalf("state=%v, want Unknown after reset", f.State())
	}
	if f.InSyncThreshold() != 50 || f.OutOfSyncThreshold() != 100 || f.SamplesRequired() != 1 {
		t.Fatalf("Reset changed thresholds: in=%v out=%v samples=%d",
			f.InSyncThreshold(), f.OutOfSyncThreshold(), f.SamplesRequired())
	}
}

// TestHysteresisFilterReusable demonstrates the filter is a general mechanism: two
// independent instances (simulating two different offset consumers) do not interfere and
// can each be driven to a different state.
func TestHysteresisFilterReusable(t *testing.T) {
	osClock := NewHysteresisFilter(50, 100, 2) // O-RAN E3 OS-clock sync
	other := NewHysteresisFilter(200, 500, 5)  // some other offset-derived metric
	osClock.Update(25)
	osClock.Update(25)
	if osClock.State() != HysteresisLow {
		t.Fatalf("osClock state=%v, want Low", osClock.State())
	}
	if other.State() != HysteresisUnknown {
		t.Fatalf("other state=%v, want Unknown (instances independent)", other.State())
	}
}
