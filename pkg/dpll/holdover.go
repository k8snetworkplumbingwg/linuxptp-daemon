package dpll

import (
	"fmt"
	"sync/atomic"
	"time"
)

var nextObservationID atomic.Uint64

func (d *DpllConfig) canEstimateHoldover() bool {
	return d.hasLeadingSource() && !d.hasFlag(FlagOnlyPhaseStatus)
}

func (d *DpllConfig) requestHoldoverEvaluation() {
	select {
	case d.holdoverWake <- struct{}{}:
	default:
	}
}

func (d *DpllConfig) endModeledHoldover() {
	d.holdoverGeneration++
	if d.holdoverCancel != nil {
		close(d.holdoverCancel)
		d.holdoverCancel = nil
	}
	d.onHoldover = false
	d.holdoverStart = time.Time{}
	d.modeledOffset, d.peakModeledOffset, d.observedElapsed = 0, 0, 0
	d.estimateValid, d.estimateErrorObserved, d.estimateError = false, false, ""
}

func (d *DpllConfig) startModeledHoldover() error {
	if d.LocalHoldoverTimeout == 0 || !d.canEstimateHoldover() {
		return fmt.Errorf("estimation disabled for this DPLL")
	}
	d.holdoverGeneration++
	d.holdoverStart = time.Now()
	d.holdoverCancel = make(chan struct{})
	d.holdoverWake = make(chan struct{}, 1)
	d.onHoldover = true
	d.wg.Add(1)
	go d.runModeledHoldover(d.holdoverGeneration, d.holdoverCancel, d.holdoverWake)
	return nil
}

func (d *DpllConfig) evaluateModeledHoldover(generation uint64, now time.Time) {
	d.modelEvaluationMu.Lock()
	defer d.modelEvaluationMu.Unlock()
	d.Lock()
	if !d.onHoldover || generation != d.holdoverGeneration {
		d.Unlock()
		return
	}
	model := d.holdoverModel
	elapsed := now.Sub(d.holdoverStart)
	failed := d.estimateErrorObserved
	d.Unlock()
	var offset int64
	var err error
	if !failed {
		offset, err = model.Estimate(elapsed)
	}
	d.Lock()
	defer d.Unlock()
	if !d.onHoldover || generation != d.holdoverGeneration {
		return
	}
	d.observedElapsed = max(d.observedElapsed, elapsed)
	if !failed {
		d.estimateValid = err == nil && offset >= 0 && elapsed >= 0
		if !d.estimateValid {
			d.estimateErrorObserved = true
			d.estimateError = fmt.Sprintf("invalid estimate %d at %s: %v", offset, elapsed, err)
		} else {
			d.modeledOffset = offset
			d.peakModeledOffset = max(d.peakModeledOffset, offset)
		}
	}
	d.sendDpllEvent()
}

func (d *DpllConfig) runModeledHoldover(generation uint64, cancel <-chan struct{}, wake <-chan struct{}) {
	defer d.wg.Done()
	ticker := time.NewTicker(monitoringInterval)
	defer ticker.Stop()
	d.evaluateModeledHoldover(generation, time.Now())
	for {
		select {
		case <-cancel:
			return
		case <-ticker.C:
			d.evaluateModeledHoldover(generation, time.Now())
		case <-wake:
			d.evaluateModeledHoldover(generation, time.Now())
		}
	}
}
