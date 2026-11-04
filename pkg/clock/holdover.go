package clock

import (
	"math"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
)

type holdoverQuality struct {
	producer uint64
	sequence uint64
	episode  uint64
	out      bool
	freerun  bool
	reset    bool
}

func dpllOffsetWithin(offset *int64, limit uint64) bool {
	return offset == nil || math.Abs(float64(*offset)) <= float64(limit)
}

func (quality *holdoverQuality) applyLegacyGNSSHoldover(data *event.DPLLData) bool {
	observation := data.Holdover
	terminal := observation.Elapsed >= time.Duration(observation.TimeoutSeconds)*time.Second ||
		(!data.FrequencyTraceable && data.Offset != nil && uint64(observation.PeakErrorNS) > observation.InSpecErrorNS)
	if observation.Valid && data.Offset != nil {
		data.Offset = utils.ToPtr(observation.EstimatedErrorNS)
	}
	return terminal
}

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

func (c *BaseClock) interpretDPLL(ev event.Event, _ bool) (event.Event, bool) {
	data, ok := ev.Data.(*event.DPLLData)
	if !ok || ev.Source != event.DPLL || data.Holdover == nil {
		return ev, true
	}
	observation := data.Holdover
	if c.holdover == nil {
		c.holdover = make(map[string]*holdoverQuality)
	}
	quality := c.holdover[ev.IFace]
	if quality != nil && quality.reset && observation.ProducerID == quality.producer && observation.Episode == quality.episode {
		return ev, false
	}
	if quality != nil && (observation.ProducerID < quality.producer ||
		(observation.ProducerID == quality.producer && observation.Sequence <= quality.sequence)) {
		return ev, false
	}
	if quality == nil || observation.ProducerID != quality.producer || observation.Episode != quality.episode {
		quality = &holdoverQuality{producer: observation.ProducerID, episode: observation.Episode}
		c.holdover[ev.IFace] = quality
	}
	quality.sequence = observation.Sequence
	interpreted := *data
	interpreted.State = observation.HardwareState
	interpreted.OutOfSpec, interpreted.ModeledHoldover = false, false
	leadingModel := (observation.Reference == event.GNSS || observation.Reference == event.PTP4l) && !observation.PhaseOnly
	switch observation.HardwareState {
	case event.PTP_LOCKED:
		quality.out, quality.freerun = false, false
		if !dpllOffsetWithin(data.Offset, observation.MaxErrorNS) {
			interpreted.State = event.PTP_FREERUN
			interpreted.OutOfSpec = true
		}
	case event.PTP_HOLDOVER:
		terminal := !leadingModel || !observation.Acquired || !dpllOffsetWithin(observation.AcquisitionOffset, observation.MaxErrorNS) || observation.TimeoutSeconds == 0 || observation.ErrorObserved || observation.Error != "" || observation.Elapsed < 0 || observation.PeakErrorNS < 0 || observation.EstimatedErrorNS < 0
		if observation.Reference == event.GNSS {
			terminal = quality.applyLegacyGNSSHoldover(&interpreted) || terminal
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
	if interpreted.State == event.PTP_FREERUN && data.Offset != nil {
		interpreted.Offset = utils.ToPtr[int64](FaultyPhaseOffset)
	}
	interpreted.SourceLost = interpreted.State != event.PTP_LOCKED
	ev.Data = &interpreted
	return ev, true
}

// DPLLReport returns the accepted clock reporting view without changing hardware observations.
func (c *BaseClock) DPLLReport(ev event.Event) (event.Event, bool) {
	data, ok := ev.Data.(*event.DPLLData)
	if !ok || data.Holdover == nil {
		return ev, true
	}
	if c.holdover[ev.IFace] == nil {
		return ev, true
	}
	var detail *event.DataDetails
	for _, stored := range c.DataHolder.ProcessData() {
		if stored.ProcessName == event.DPLL {
			detail = stored.GetDataDetails(ev.IFace)
			break
		}
	}
	if detail == nil || detail.Holdover == nil || detail.Holdover.ProducerID != data.Holdover.ProducerID || detail.Holdover.Sequence != data.Holdover.Sequence {
		return ev, false
	}
	reported := *data
	reported.Offset = nil
	if detail.HasOffset {
		reported.Offset = utils.ToPtr(detail.Offset)
	}
	reported.State, reported.OutOfSpec = detail.State, detail.OutOfSpec
	reported.SourceLost, reported.ModeledHoldover = detail.SourceLost, detail.ModeledHoldover
	ev.Data = &reported
	return ev, true
}
