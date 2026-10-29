package dpll

import (
	"fmt"
	"math"
	"reflect"
	"time"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
)

func (d *DpllConfig) usesHoldoverModel() bool {
	return d.processConfig.ClockType == event.GM && len(d.dependsOn) > 0 &&
		d.dependsOn[0] == event.GNSS && !d.hasFlag(FlagOnlyPhaseStatus)
}

func (d *DpllConfig) ReplaceHoldoverModel(model HoldoverModel) error {
	if model == nil {
		return fmt.Errorf("nil holdover model")
	}
	value := reflect.ValueOf(model)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return fmt.Errorf("nil holdover model")
		}
	}
	d.Lock()
	defer d.Unlock()
	if !d.usesHoldoverModel() {
		return fmt.Errorf("holdover model requires a leading GNSS T-GM DPLL")
	}
	d.holdoverModel = model
	d.modelRevision++
	d.requestHoldoverEvaluation()
	return nil
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
	d.modeledOffset = 0
}

func (d *DpllConfig) failModeledHoldover() {
	d.endModeledHoldover()
	d.holdoverAcquired = false
	d.inSpec = false
	d.sourceLost = true
	d.state = event.PTP_FREERUN
}

func (d *DpllConfig) startModeledHoldover() error {
	if d.LocalHoldoverTimeout == 0 {
		return fmt.Errorf("holdover is disabled")
	}
	if d.LocalMaxHoldoverOffSet == 0 || d.LocalHoldoverTimeout > uint64(math.MaxInt64/int64(time.Second)) {
		return fmt.Errorf("invalid active linear holdover configuration: offset=%d timeout=%d", d.LocalMaxHoldoverOffSet, d.LocalHoldoverTimeout)
	}
	if d.holdoverModel == nil {
		model, err := NewLinearHoldoverModel(d.LocalMaxHoldoverOffSet, time.Duration(d.LocalHoldoverTimeout)*time.Second)
		if err != nil {
			return err
		}
		d.holdoverModel = model
		d.modelRevision++
	}
	d.holdoverGeneration++
	d.holdoverStart = time.Now()
	d.holdoverCancel = make(chan struct{})
	d.holdoverWake = make(chan struct{}, 1)
	d.onHoldover = true
	d.inSpec = true
	d.sourceLost = true
	d.state = event.PTP_HOLDOVER
	d.wg.Add(1)
	go d.runModeledHoldover(d.holdoverGeneration, d.holdoverCancel, d.holdoverWake)
	return nil
}

func (d *DpllConfig) modeledStateDecision() {
	if d.processState == process.Stopping || d.processState == process.Stopped || d.processState == process.Dead {
		d.failModeledHoldover()
		d.sendDpllEvent()
		return
	}
	switch d.getDpllState() {
	case DPLL_LOCKED, DPLL_LOCKED_HO_ACQ:
		if d.LocalHoldoverTimeout == 0 || d.LocalMaxHoldoverOffSet == 0 || d.isOffsetInRange() {
			d.endModeledHoldover()
			d.holdoverAcquired = true
			d.inSpec = true
			d.sourceLost = false
			d.state = event.PTP_LOCKED
		} else {
			d.failModeledHoldover()
		}
	case DPLL_HOLDOVER:
		if !d.holdoverAcquired {
			d.failModeledHoldover()
		} else if !d.onHoldover {
			if err := d.startModeledHoldover(); err != nil {
				glog.Errorf("%s: %v", d.iface, err)
				d.failModeledHoldover()
			}
		} else {
			d.requestHoldoverEvaluation()
		}
	default:
		d.failModeledHoldover()
	}
	d.sendDpllEvent()
}

func (d *DpllConfig) evaluateModeledHoldover(generation uint64, now time.Time) {
	d.modelEvaluationMu.Lock()
	defer d.modelEvaluationMu.Unlock()
	d.Lock()
	if !d.onHoldover || generation != d.holdoverGeneration {
		d.Unlock()
		return
	}
	model, revision := d.holdoverModel, d.modelRevision
	elapsed := now.Sub(d.holdoverStart)
	d.Unlock()
	offset, err := model.Estimate(elapsed)
	d.Lock()
	defer d.Unlock()
	if !d.onHoldover || generation != d.holdoverGeneration || revision != d.modelRevision {
		return
	}
	if err != nil || offset < 0 {
		glog.Errorf("%s: invalid holdover estimate %d: %v", d.iface, offset, err)
		d.failModeledHoldover()
	} else if uint64(offset) > d.LocalMaxHoldoverOffSet {
		d.failModeledHoldover()
	} else {
		d.modeledOffset = offset
		if uint64(offset) > d.MaxInSpecOffset || elapsed > time.Duration(d.LocalHoldoverTimeout)*time.Second {
			d.inSpec = false
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
