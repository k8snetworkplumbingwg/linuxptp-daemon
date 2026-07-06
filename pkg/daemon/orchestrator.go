package daemon

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/golang/glog"
)

// StartupPhase defines one step in the startup sequence.
type StartupPhase struct {
	Name         string
	ProcessNames []string
	Precondition StartupPrecondition
}

// ProcessFailureSemantics defines how the system reacts when a specific process fails.
type ProcessFailureSemantics struct {
	EnterHoldover     bool
	FlipSyncDirection bool
	EmitE1Always      bool
	SuppressE3Always  bool
	SilentRestart     bool
}

// StartupPlan is the complete startup configuration for a clock type.
type StartupPlan struct {
	ClockType         string
	Phases            []StartupPhase
	FailureSemantics  map[string]ProcessFailureSemantics
	DefaultThresholds map[string]time.Duration
}

// OrchestratorOption configures the ProcessOrchestrator.
type OrchestratorOption func(*ProcessOrchestrator)

// WithPollInterval sets the precondition polling interval.
func WithPollInterval(d time.Duration) OrchestratorOption {
	return func(o *ProcessOrchestrator) {
		o.pollInterval = d
	}
}

// WithClock injects a clock source for testability.
func WithClock(c Clock) OrchestratorOption {
	return func(o *ProcessOrchestrator) {
		o.clock = c
	}
}

// Clock abstracts time operations for testability.
type Clock interface {
	Now() time.Time
	NewTicker(d time.Duration) Ticker
}

// Ticker abstracts time.Ticker for testability.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

type realClock struct{}

func (realClock) Now() time.Time                   { return time.Now() }
func (realClock) NewTicker(d time.Duration) Ticker { return &realTicker{ticker: time.NewTicker(d)} }

type realTicker struct{ ticker *time.Ticker }

func (t *realTicker) C() <-chan time.Time { return t.ticker.C }
func (t *realTicker) Stop()               { t.ticker.Stop() }

type managedProcess struct {
	name    string
	startFn func() error
	stopFn  func()
}

// ProcessOrchestrator manages phased process startup and lifecycle.
type ProcessOrchestrator struct {
	mu           sync.Mutex
	plan         StartupPlan
	pollInterval time.Duration
	clock        Clock
	processes    map[string]*managedProcess
	running      map[string]bool
	started      bool
	cancel       context.CancelFunc
	done         chan struct{}
	downtime     *DowntimeTracker
}

// NewProcessOrchestrator creates an orchestrator from a startup plan.
func NewProcessOrchestrator(plan *StartupPlan, opts ...OrchestratorOption) *ProcessOrchestrator {
	o := &ProcessOrchestrator{
		plan:         *plan,
		pollInterval: 500 * time.Millisecond,
		clock:        realClock{},
		processes:    make(map[string]*managedProcess),
		running:      make(map[string]bool),
		done:         make(chan struct{}),
		downtime:     NewDowntimeTracker(plan.DefaultThresholds),
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// RegisterProcess registers actual start/stop callbacks for a named process.
func (o *ProcessOrchestrator) RegisterProcess(name string, startFn func() error, stopFn func()) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.processes[name] = &managedProcess{name: name, startFn: startFn, stopFn: stopFn}
}

// Start begins phased startup, blocking until all phases complete or ctx is cancelled.
func (o *ProcessOrchestrator) Start(ctx context.Context) error {
	o.mu.Lock()
	if o.started {
		o.mu.Unlock()
		return fmt.Errorf("orchestrator already started")
	}
	o.started = true
	ctx, o.cancel = context.WithCancel(ctx)
	o.mu.Unlock()

	defer close(o.done)
	return o.run(ctx)
}

func (o *ProcessOrchestrator) run(ctx context.Context) error {
	for i, phase := range o.plan.Phases {
		if err := o.waitForPrecondition(ctx, phase); err != nil {
			glog.Warningf("orchestrator: phase %q cancelled: %v", phase.Name, err)
			return fmt.Errorf("phase %q: %w", phase.Name, err)
		}
		if ctx.Err() != nil {
			return fmt.Errorf("phase %q: %w", phase.Name, ctx.Err())
		}

		glog.Infof("orchestrator: starting phase %d/%d %q", i+1, len(o.plan.Phases), phase.Name)
		if err := o.startPhaseProcesses(phase); err != nil {
			glog.Errorf("orchestrator: phase %q failed: %v", phase.Name, err)
			return fmt.Errorf("phase %q: %w", phase.Name, err)
		}
	}
	glog.Infof("orchestrator: all %d phases completed for %s", len(o.plan.Phases), o.plan.ClockType)
	return nil
}

func (o *ProcessOrchestrator) waitForPrecondition(ctx context.Context, phase StartupPhase) error {
	if phase.Precondition == nil || phase.Precondition.Satisfied() {
		return nil
	}

	ticker := o.clock.NewTicker(o.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for precondition %q: %w",
				phase.Precondition.Name(), ctx.Err())
		case <-ticker.C():
			if phase.Precondition.Satisfied() {
				return nil
			}
		}
	}
}

func (o *ProcessOrchestrator) startPhaseProcesses(phase StartupPhase) error {
	for _, name := range phase.ProcessNames {
		o.mu.Lock()
		proc, exists := o.processes[name]
		o.mu.Unlock()
		if !exists {
			return fmt.Errorf("process %q not registered", name)
		}

		if err := proc.startFn(); err != nil {
			return fmt.Errorf("starting process %q: %w", name, err)
		}
		o.mu.Lock()
		o.running[name] = true
		o.mu.Unlock()
		glog.Infof("orchestrator: process %q started", name)
	}
	return nil
}

// Stop performs graceful shutdown of all managed processes in reverse order.
func (o *ProcessOrchestrator) Stop() {
	o.mu.Lock()
	if o.cancel != nil {
		o.cancel()
	}
	o.mu.Unlock()

	<-o.done

	o.mu.Lock()
	defer o.mu.Unlock()

	for i := len(o.plan.Phases) - 1; i >= 0; i-- {
		phase := o.plan.Phases[i]
		for j := len(phase.ProcessNames) - 1; j >= 0; j-- {
			name := phase.ProcessNames[j]
			if proc, exists := o.processes[name]; exists && o.running[name] {
				proc.stopFn()
				o.running[name] = false
				glog.Infof("orchestrator: process %q stopped", name)
			}
		}
	}
}

// OnProcessExit records a process failure and returns the applicable failure semantics.
func (o *ProcessOrchestrator) OnProcessExit(processName string, exitTime time.Time) ProcessFailureSemantics {
	o.mu.Lock()
	o.running[processName] = false
	o.mu.Unlock()

	o.downtime.RecordFailure(processName, exitTime)

	semantics, exists := o.plan.FailureSemantics[processName]
	if !exists {
		return ProcessFailureSemantics{}
	}
	glog.Infof("orchestrator: process %q exited at %v, semantics: %+v", processName, exitTime, semantics)
	return semantics
}

// OnProcessRestart marks a process as running again and evaluates downtime.
// Preconditions are bypassed on restart per FR-7 (system was already qualified).
func (o *ProcessOrchestrator) OnProcessRestart(processName string, restartTime time.Time) DowntimeResult {
	o.mu.Lock()
	o.running[processName] = true
	o.mu.Unlock()

	result := o.downtime.EvaluateRestart(processName, restartTime)
	glog.Infof("orchestrator: process %q restarted at %v (downtime=%v, exceeded=%v)",
		processName, restartTime, result.Downtime, result.ThresholdExceeded)
	return result
}

// IsRunning implements ProcessStateProvider.
func (o *ProcessOrchestrator) IsRunning(processName string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.running[processName]
}

// DowntimeTracker returns the orchestrator's downtime tracker for use by event gates.
func (o *ProcessOrchestrator) DowntimeTracker() *DowntimeTracker {
	return o.downtime
}

// Done returns a channel that closes when the orchestrator's run loop completes.
func (o *ProcessOrchestrator) Done() <-chan struct{} {
	return o.done
}
