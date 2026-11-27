package clock

import (
	"fmt"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	parserconstants "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
)

var sourceGeneration atomic.Uint64

const maxPendingSourceActions = 100

type sourceInputAction struct {
	generation uint64
	qualified  bool
}

// SourceInputActions serializes input actions without blocking the clock loop.
type SourceInputActions struct {
	mu               sync.Mutex
	closed           bool
	generation       uint64
	configName       string
	profileName      string
	apply            func(bool) error
	desired          bool
	desiredKnown     bool
	pendingActions   chan sourceInputAction
	workerDone       chan struct{}
	activeGeneration uint64
	activeDone       chan struct{}
}

func (w *SourceInputActions) reset(generation uint64, clearAction bool) {
	// Clock configuration, the worker, and teardown share the generation and queue.
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.generation = generation
	w.desiredKnown = false
	w.pendingActions = make(chan sourceInputAction, maxPendingSourceActions)
	if clearAction {
		w.apply = nil
		w.profileName = ""
	}
}

func (w *SourceInputActions) configure(generation uint64, configName, profileName string, apply func(bool) error) error {
	// Process setup, the worker, and teardown share the action binding.
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.generation != generation {
		return fmt.Errorf("source qualification changed for %s", configName)
	}
	if apply == nil {
		return fmt.Errorf("source input action is required for %s", configName)
	}
	w.configName = configName
	w.profileName = profileName
	w.apply = apply
	return nil
}

func (w *SourceInputActions) observe(result event.SourceQualification) {
	// The clock loop, worker, and teardown share the desired state and queue.
	w.mu.Lock()
	if w.closed || w.apply == nil || result.Generation != w.generation {
		w.mu.Unlock()
		return
	}
	inputChanged := !w.desiredKnown || w.desired != result.Qualified
	overflowed := false
	if inputChanged {
		w.desired = result.Qualified
		w.desiredKnown = true
		select {
		case w.pendingActions <- sourceInputAction{generation: result.Generation, qualified: result.Qualified}:
			if w.workerDone == nil {
				w.workerDone = make(chan struct{})
				go w.run(w.workerDone)
			}
		default:
			overflowed = true
		}
	}
	configName := w.configName
	profileName := w.profileName
	w.mu.Unlock()
	if result.Changed {
		glog.Infof("PTPSourceQualified profile=%s config=%s source=%s port=%s qualified=%t reason=%q samples=%d",
			profileName, configName, result.SourceType, result.IFace, result.Qualified, result.Reason, result.Samples)
	}
	if overflowed {
		glog.Errorf("SOURCE INPUT ACTION QUEUE FULL for %s: DROPPING action qualified=%t", configName, result.Qualified)
	}
}

func (w *SourceInputActions) run(done chan struct{}) {
	for {
		// The worker, clock loop, and teardown share pending and active actions.
		w.mu.Lock()
		var action sourceInputAction
		select {
		case action = <-w.pendingActions:
		default:
			w.workerDone = nil
			close(done)
			w.mu.Unlock()
			return
		}
		if action.generation != w.generation || w.apply == nil || (w.closed && action.qualified) {
			w.mu.Unlock()
			continue
		}
		apply := w.apply
		profileName := w.profileName
		actionDone := make(chan struct{})
		w.activeGeneration = action.generation
		w.activeDone = actionDone
		w.mu.Unlock()
		if err := apply(action.qualified); err != nil {
			glog.Errorf("PTPSourceQualified hardware apply profile=%s qualified=%t: %v", profileName, action.qualified, err)
		}
		// The worker and teardown share completion of the in-flight action.
		w.mu.Lock()
		w.activeGeneration = 0
		w.activeDone = nil
		close(actionDone)
		w.mu.Unlock()
	}
}

// Done returns the current worker's completion channel, or nil when idle.
func (w *SourceInputActions) Done() <-chan struct{} {
	// Callers and the worker share the current completion channel.
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.workerDone
}

// Close cancels pending enables and waits for any in-flight action and pending loss.
func (w *SourceInputActions) Close() { w.close(0, false) }

// CloseGeneration retires this source without stopping a replacement generation.
func (w *SourceInputActions) CloseGeneration(generation uint64) { w.close(generation, true) }

func (w *SourceInputActions) close(generation uint64, checkGeneration bool) {
	// Teardown, the clock loop, and the worker share lifecycle and action state.
	w.mu.Lock()
	if checkGeneration && w.generation != generation {
		var done <-chan struct{}
		if w.activeGeneration == generation {
			done = w.activeDone
		}
		w.mu.Unlock()
		if done != nil {
			<-done
		}
		return
	}
	w.closed = true
	done := w.workerDone
	w.mu.Unlock()
	if done != nil {
		<-done
	}
}

// PTPPortSource identifies a physical upstream PTP port. Other input kinds can
// implement utils.QualificationSource without depending on PTP port names.
type PTPPortSource struct {
	Port string
}

// Equal reports whether other identifies the same PTP port.
func (s PTPPortSource) Equal(other utils.QualificationSource) bool {
	port, ok := other.(PTPPortSource)
	return ok && s.Port == port.Port
}

// String returns the PTP port's interface name.
func (s PTPPortSource) String() string { return s.Port }

type ptpSourceQualification struct {
	state                   *utils.QualificationState
	configuredUpstreamPorts []string
	lastSelectedPort        string
	available               bool
	generation              uint64
	observed                bool
}

func (qlf *ptpSourceQualification) source() utils.QualificationSource {
	if qlf.lastSelectedPort == "" {
		return nil
	}
	return PTPPortSource{Port: qlf.lastSelectedPort}
}

// ConfigureSourceQualification installs the upstream policy before processes start.
func (c *TBC) ConfigureSourceQualification(configuredUpstreamPorts []string, policy utils.QualificationConfig) (uint64, error) {
	state, err := utils.NewQualificationState(policy)
	if err == nil && len(configuredUpstreamPorts) == 0 {
		err = fmt.Errorf("source qualification requires upstream TR ports")
	}
	generation := uint64(0)
	if err == nil {
		generation = sourceGeneration.Add(1)
	}
	c.sourceActions.reset(generation, true)
	c.ptpSource = nil
	if err != nil {
		return 0, err
	}
	c.ptpSource = &ptpSourceQualification{
		state: state, configuredUpstreamPorts: slices.Clone(configuredUpstreamPorts), generation: generation,
	}
	return generation, nil
}

// SetSourceInputAction connects the clock-owned worker to the configured hardware action.
func (c *TBC) SetSourceInputAction(profileName string, apply func(bool) error) (*SourceInputActions, error) {
	if c.ptpSource == nil {
		return nil, fmt.Errorf("source qualification is not configured for %s", c.cfgName)
	}
	if err := c.sourceActions.configure(c.ptpSource.generation, c.cfgName, profileName, apply); err != nil {
		return nil, err
	}
	return &c.sourceActions, nil
}

// SourceQualification returns clock-owned upstream readiness, independently of aggregate InSync.
func (c *TBC) SourceQualification(cfg string) (event.SourceQualification, bool) {
	qlf := c.ptpSource
	if qlf == nil || cfg != c.cfgName {
		return event.SourceQualification{}, false
	}
	return event.SourceQualification{SourceType: event.PTP4l, IFace: qlf.lastSelectedPort, Generation: qlf.generation,
		QualificationResult: utils.QualificationResult{Source: qlf.source(), Qualified: qlf.state.Qualified()}}, true
}

// ObserveSource evaluates a raw observation on the serialized clock loop.
func (c *TBC) ObserveSource(ev event.Event, observation *event.PTPSourceObservation) event.SourceQualification {
	qlf := c.ptpSource
	if qlf == nil || ev.Source != event.PTP4l || ev.CfgName != c.cfgName || observation.Generation != qlf.generation {
		return event.SourceQualification{}
	}
	wasQualified := qlf.state.Qualified()
	wasAvailable := qlf.available
	var result utils.QualificationResult
	switch {
	case observation.SourceLost:
		qlf.available = false
		result = qlf.state.Reset()
	case observation.PortRole != nil:
		role := *observation.PortRole
		if !slices.Contains(qlf.configuredUpstreamPorts, ev.IFace) || role == parserconstants.PortRoleUnknown {
			return event.SourceQualification{}
		}
		if role == parserconstants.PortRoleSlave {
			if qlf.lastSelectedPort == ev.IFace && qlf.available {
				return event.SourceQualification{}
			}
			qlf.lastSelectedPort = ev.IFace
			qlf.available = true
			// Old upstream details must not masquerade as a still-locked source.
			d := c.GetData(event.PTP4l)
			d.Details = nil
			d.Window = utils.NewWindow(event.WindowSize)
		} else if qlf.lastSelectedPort == ev.IFace && qlf.available {
			qlf.available = false
		} else {
			return event.SourceQualification{}
		}
		result = qlf.state.Reset()
	default:
		if observation.ServoState == "" || (ev.IFace != "" && ev.IFace != "master" && ev.IFace != qlf.lastSelectedPort) {
			return event.SourceQualification{}
		}
		result = qlf.state.Evaluate(utils.QualificationSample{Source: qlf.source(), Offset: observation.Offset, ServoState: observation.ServoState,
			SourceAvailable: qlf.available && (observation.ServoState == "s2" || observation.ServoState == "s3")})
	}
	if observation.ClockID != "" {
		c.leadingClockData.clockID = observation.ClockID
	}
	qlf.observed = true
	decision := event.SourceQualification{SourceType: event.PTP4l, IFace: qlf.lastSelectedPort, Generation: qlf.generation, QualificationResult: result}
	c.sourceActions.observe(decision)
	if qlf.state.Qualified() != wasQualified || qlf.available != wasAvailable || observation.SourceLost {
		c.evaluateState()
	}
	return decision
}
