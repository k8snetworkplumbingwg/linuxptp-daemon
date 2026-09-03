package utils

import (
	"math"
)

// HysteresisState is the state a HysteresisFilter emits after classifying deviation
// samples. It is intentionally a plain, dependency-free enum so any consumer can map it
// onto its own domain (e.g. the daemon's event.PTPState).
type HysteresisState int

const (
	// HysteresisUnknown is the initial state of a fresh filter ("not set").
	HysteresisUnknown HysteresisState = iota
	// HysteresisLow means abs(deviation) <= inSyncThreshold for samplesRequired
	// consecutive samples (i.e. the deviation is below the in-sync threshold).
	HysteresisLow
	// HysteresisHigh means abs(deviation) > outOfSyncThreshold for samplesRequired
	// consecutive samples (i.e. the deviation is above the out-of-sync threshold).
	HysteresisHigh
)

// HysteresisFilter applies hysteresis to any scalar deviation metric (an offset or
// timestamp error, in the same units) compared against two thresholds. It only flips
// between Low and High once abs(deviation) has breached the matching threshold for
// samplesRequired consecutive samples, so a single jitter sample crossing a threshold
// line does not flap the state. Deviations inside the hysteresis band (between the two
// thresholds) reset both consecutive-sample counters.
//
// The filter is fully generic and depends only on the standard library: it has no
// knowledge of the daemon's event types, phc2sys, the OS clock, or the PtpClockThreshold
// API. Any consumer configures it via NewHysteresisFilter or Config, feeds deviation
// samples via Update, and maps the resulting HysteresisState onto its own domain.
// Non-numerical conditions that are not derived from the deviation (e.g. a caller's raw
// clock-state passthrough) are the caller's concern and are handled outside the filter.
type HysteresisFilter struct {
	inSyncThreshold    float64
	outOfSyncThreshold float64
	samplesRequired    int
	lastState          HysteresisState
	inSyncCount        int
	outOfSyncCount     int
}

// NewHysteresisFilter returns a filter with the given thresholds (in the deviation's
// units) and required consecutive samples, starting in the Unknown state.
func NewHysteresisFilter(inSyncThreshold, outOfSyncThreshold float64, samplesRequired int) *HysteresisFilter {
	f := &HysteresisFilter{}
	f.Config(inSyncThreshold, outOfSyncThreshold, samplesRequired)
	return f
}

// Config sets the thresholds and the number of consecutive samples required for a
// transition, and resets the consecutive-sample counters. lastState is left untouched so
// an already-running filter can be reconfigured without losing its current state; a fresh
// (zero-value) filter starts in HysteresisUnknown.
func (f *HysteresisFilter) Config(inSyncThreshold, outOfSyncThreshold float64, samplesRequired int) {
	f.inSyncThreshold = inSyncThreshold
	f.outOfSyncThreshold = outOfSyncThreshold
	f.samplesRequired = samplesRequired
	f.inSyncCount, f.outOfSyncCount = 0, 0
}

// Reset clears the consecutive-sample counters and returns the filter to the Unknown
// state, ready for a fresh evaluation window. Thresholds and samplesRequired are kept.
func (f *HysteresisFilter) Reset() {
	f.inSyncCount, f.outOfSyncCount = 0, 0
	f.lastState = HysteresisUnknown
}

// Update feeds one deviation sample through the filter, returning the resulting
// HysteresisState and whether it transitioned. See the type doc for the
// threshold/consecutive-sample semantics.
func (f *HysteresisFilter) Update(rawDeviation float64) (HysteresisState, bool) {
	absValue := math.Abs(rawDeviation)
	switch {
	case absValue <= f.inSyncThreshold:
		f.outOfSyncCount = 0
		f.inSyncCount++
	case absValue > f.outOfSyncThreshold:
		f.inSyncCount = 0
		f.outOfSyncCount++
	default: // in the hysteresis band: reset both counters
		f.inSyncCount, f.outOfSyncCount = 0, 0
	}
	next := f.lastState
	if f.inSyncCount >= f.samplesRequired {
		next = HysteresisLow
	} else if f.outOfSyncCount >= f.samplesRequired {
		next = HysteresisHigh
	}
	changed := next != f.lastState
	f.lastState = next
	return next, changed
}

// InSyncThreshold returns the configured in-sync threshold.
func (f *HysteresisFilter) InSyncThreshold() float64 {
	return f.inSyncThreshold
}

// OutOfSyncThreshold returns the configured out-of-sync threshold.
func (f *HysteresisFilter) OutOfSyncThreshold() float64 {
	return f.outOfSyncThreshold
}

// SamplesRequired returns the configured number of consecutive samples required for a
// transition.
func (f *HysteresisFilter) SamplesRequired() int {
	return f.samplesRequired
}

// State returns the filter's current state.
func (f *HysteresisFilter) State() HysteresisState {
	return f.lastState
}
