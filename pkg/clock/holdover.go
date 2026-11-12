package clock

import (
	"math"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
)

// holdoverQuality tracks an interface's admission watermark and episode quality
// latches. reset rejects the remembered producer/episode after a clock reset.
type holdoverQuality struct {
	producer uint64
	sequence uint64
	episode  uint64
	out      bool
	freerun  bool
	reset    bool
}

// dpllOffsetWithin reports whether no present offset exceeds the inclusive limit.
// A nil offset is absent, not a measured zero or proof of an in-range sample.
func dpllOffsetWithin(offset *int64, limit uint64) bool {
	return offset == nil || math.Abs(float64(*offset)) <= float64(limit)
}

// applyGMHoldover latches T-GM out-of-spec quality from elapsed/peak facts and
// marks modeled holdover on the copied clock view without replacing its measured
// offset (including nil). True requests terminal FREERUN, not sample acceptance;
// false may still be out of spec or fail the caller's other terminal checks.
// The raw observation is unchanged.
func (quality *holdoverQuality) applyGMHoldover(data *event.DPLLData) bool {
	observation := data.Holdover
	if observation.Elapsed > time.Duration(observation.TimeoutSeconds)*time.Second || uint64(observation.PeakErrorNS) > observation.InSpecErrorNS {
		quality.out = true
	}
	data.ModeledHoldover = observation.Active
	return observation.MaxErrorNS == 0 || observation.TimeoutSeconds > uint64(math.MaxInt64/int64(time.Second)) || uint64(observation.PeakErrorNS) > observation.MaxErrorNS
}

// applyLegacyHoldover applies legacy reference-dependent error/timeout limits,
// latching out-of-spec quality and replacing a present offset with a valid estimate
// in the copied clock view. Nil stays absent; the raw observation is unchanged.
// True means holdover must end and is latched by the caller, not sample acceptance.
func (quality *holdoverQuality) applyLegacyHoldover(data *event.DPLLData) bool {
	observation := data.Holdover
	terminal := observation.Elapsed >= time.Duration(observation.TimeoutSeconds)*time.Second ||
		(data.Offset != nil && uint64(observation.PeakErrorNS) > observation.MaxErrorNS)
	if observation.HardwareSample && (quality.out || !dpllOffsetWithin(data.Offset, observation.MaxErrorNS)) {
		terminal = true
	}
	if uint64(observation.PeakErrorNS) > observation.InSpecErrorNS && data.Offset != nil {
		if observation.Reference == event.GNSS && !data.FrequencyTraceable {
			terminal = true
		} else {
			quality.out = true
		}
	}
	if observation.Valid && data.Offset != nil {
		data.Offset = utils.ToPtr(observation.EstimatedErrorNS)
	}
	return terminal
}

// prepareDPLL derives distinct policy and reporting views from an observation and
// updates per-interface admission watermarks and quality latches. It copies the
// DPLLData, leaving the input and shared raw Holdover observation unchanged; nil
// offsets remain absent. Non-observation events pass through unchanged with true.
// False skips processing older producers, non-increasing sequences, or a reset's remembered
// producer/episode before aggregation. True permits processing even if the copied
// state is FREERUN; it does not mean in-spec. Eligible GM GNSS observations use
// T-GM policy; other observations retain legacy policy.
// Accepted reporting fields are fixed here: storage copies them unchanged and
// subsequent aggregation updates composite state, not the per-interface sample.
func (c *BaseClock) prepareDPLL(ev event.Event, gm bool) (event.Event, EventResult, bool) {
	data, ok := ev.Data.(*event.DPLLData)
	if !ok || ev.Source != event.DPLL || data.Holdover == nil {
		return ev, c.retainedDPLLResult(ev), true
	}
	observation := data.Holdover
	if c.holdover == nil {
		c.holdover = make(map[string]*holdoverQuality)
	}
	quality := c.holdover[ev.IFace]
	if quality != nil && quality.reset && observation.ProducerID == quality.producer && observation.Episode == quality.episode {
		return ev, c.retainedDPLLResult(ev), false
	}
	if quality != nil && (observation.ProducerID < quality.producer ||
		(observation.ProducerID == quality.producer && observation.Sequence <= quality.sequence)) {
		return ev, c.retainedDPLLResult(ev), false
	}
	if quality == nil || observation.ProducerID != quality.producer || observation.Episode != quality.episode {
		quality = &holdoverQuality{producer: observation.ProducerID, episode: observation.Episode}
		c.holdover[ev.IFace] = quality
	}
	quality.sequence = observation.Sequence
	interpreted := *data
	interpreted.State = observation.HardwareState
	interpreted.OutOfSpec, interpreted.ModeledHoldover = false, false
	modeledGM := gm && observation.Reference == event.GNSS && !observation.PhaseOnly
	leadingModel := (observation.Reference == event.GNSS || observation.Reference == event.PTP4l) && !observation.PhaseOnly
	switch observation.HardwareState {
	case event.PTP_LOCKED:
		quality.out, quality.freerun = false, false
		if !dpllOffsetWithin(data.Offset, observation.MaxErrorNS) && (!modeledGM || (observation.TimeoutSeconds != 0 && observation.MaxErrorNS != 0)) {
			interpreted.State = event.PTP_FREERUN
			interpreted.OutOfSpec = true
		}
	case event.PTP_HOLDOVER:
		terminal := !leadingModel || !observation.Acquired || !dpllOffsetWithin(observation.AcquisitionOffset, observation.MaxErrorNS) || observation.TimeoutSeconds == 0 || observation.ErrorObserved || observation.Error != "" || observation.Elapsed < 0 || observation.PeakErrorNS < 0 || observation.EstimatedErrorNS < 0
		if modeledGM {
			terminal = quality.applyGMHoldover(&interpreted) || terminal
		} else {
			terminal = quality.applyLegacyHoldover(&interpreted) || terminal
		}
		quality.freerun = quality.freerun || terminal
		if quality.freerun {
			interpreted.State = event.PTP_FREERUN
		}
		interpreted.OutOfSpec = quality.out || quality.freerun
	default:
		quality.out, quality.freerun = false, false
		interpreted.State = event.PTP_FREERUN
		interpreted.OutOfSpec = true
	}
	if !modeledGM && interpreted.State == event.PTP_FREERUN && data.Offset != nil {
		interpreted.Offset = utils.ToPtr[int64](FaultyPhaseOffset)
	}
	interpreted.SourceLost = interpreted.State != event.PTP_LOCKED
	result := EventResult{Report: dpllReportingEvent(ev, interpreted)}
	ev.Data = &interpreted
	return ev, result, true
}

// retainedDPLLResult preserves the accepted snapshot for an exact duplicate.
// Only unprepared observations need a storage lookup: an older sample or a reset
// cannot report a different accepted identity, and a duplicate cannot substitute
// its new model facts for the accepted sample's offset or quality.
func (c *BaseClock) retainedDPLLResult(ev event.Event) EventResult {
	data, ok := ev.Data.(*event.DPLLData)
	if !ok || data.Holdover == nil || c.holdover[ev.IFace] == nil {
		return EventResult{}
	}
	result := EventResult{SuppressReport: true}
	var detail *event.DataDetails
	for _, stored := range c.DataHolder.ProcessData() {
		if stored.ProcessName == event.DPLL {
			detail = stored.GetDataDetails(ev.IFace)
			break
		}
	}
	if detail == nil || detail.Holdover == nil || detail.Holdover.ProducerID != data.Holdover.ProducerID || detail.Holdover.Sequence != data.Holdover.Sequence {
		return result
	}
	policy := event.DPLLData{State: detail.State, OutOfSpec: detail.OutOfSpec,
		SourceLost: detail.SourceLost, ModeledHoldover: detail.ModeledHoldover, Holdover: detail.Holdover}
	if detail.HasOffset {
		policy.Offset = utils.ToPtr(detail.Offset)
	}
	return EventResult{Report: dpllReportingEvent(ev, policy)}
}

// dpllReportingEvent owns a separate offset value while retaining the raw event
// metadata and observation. Policy data remains available unchanged to aggregation.
// Present offsets report the measurement when locked, a valid estimate in
// holdover, or the terminal sentinel in free run; absent offsets remain absent.
func dpllReportingEvent(ev event.Event, policy event.DPLLData) *event.Event {
	data := ev.Data.(*event.DPLLData)
	reported := *data
	reported.Offset = nil
	if policy.Offset != nil {
		reported.Offset = utils.ToPtr(*policy.Offset)
		switch policy.State {
		case event.PTP_HOLDOVER:
			if policy.Holdover.Valid {
				reported.Offset = utils.ToPtr(policy.Holdover.EstimatedErrorNS)
			}
		case event.PTP_FREERUN:
			reported.Offset = utils.ToPtr[int64](FaultyPhaseOffset)
		}
	}
	reported.State, reported.OutOfSpec = policy.State, policy.OutOfSpec
	reported.SourceLost, reported.ModeledHoldover = policy.SourceLost, policy.ModeledHoldover
	ev.Data = &reported
	return &ev
}
