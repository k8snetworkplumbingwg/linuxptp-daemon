package daemon

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

const testClockType = "test"

type fakeTicker struct {
	ch   chan time.Time
	done chan struct{}
}

func (f *fakeTicker) C() <-chan time.Time { return f.ch }
func (f *fakeTicker) Stop()               { close(f.done) }

type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	tickers []*fakeTicker
}

func newFakeClock(now time.Time) *fakeClock {
	return &fakeClock{now: now}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTicker(_ time.Duration) Ticker {
	c.mu.Lock()
	defer c.mu.Unlock()
	ft := &fakeTicker{
		ch:   make(chan time.Time, 10),
		done: make(chan struct{}),
	}
	c.tickers = append(c.tickers, ft)
	return ft
}

func (c *fakeClock) Tick() {
	c.mu.Lock()
	tickers := append([]*fakeTicker(nil), c.tickers...)
	c.mu.Unlock()
	for _, t := range tickers {
		select {
		case t.ch <- c.Now():
		default:
		}
	}
}

type startRecord struct {
	mu    sync.Mutex
	order []string
}

func (r *startRecord) append(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.order = append(r.order, name)
}

func (r *startRecord) get() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

type controllablePrecondition struct {
	mu        sync.Mutex
	satisfied bool
}

func (c *controllablePrecondition) Satisfied() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.satisfied
}

func (c *controllablePrecondition) Name() string { return "controllable" }

func (c *controllablePrecondition) setSatisfied(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.satisfied = v
}

func TestOrchestrator_PhasedStartup(t *testing.T) {
	t.Parallel()
	clock := newFakeClock(time.Now())
	record := &startRecord{}

	preCond2 := &controllablePrecondition{satisfied: false}
	preCond3 := &controllablePrecondition{satisfied: false}

	plan := &StartupPlan{
		ClockType: testClockType,
		Phases: []StartupPhase{
			{Name: "phase-1", ProcessNames: []string{"proc-a"}, Precondition: &NoPrecondition{}},
			{Name: "phase-2", ProcessNames: []string{"proc-b"}, Precondition: preCond2},
			{Name: "phase-3", ProcessNames: []string{"proc-c"}, Precondition: preCond3},
		},
		FailureSemantics:  map[string]ProcessFailureSemantics{},
		DefaultThresholds: map[string]time.Duration{},
	}

	orch := NewProcessOrchestrator(plan, WithClock(clock), WithPollInterval(10*time.Millisecond))

	for _, name := range []string{"proc-a", "proc-b", "proc-c"} {
		n := name
		orch.RegisterProcess(n, func() error {
			record.append(n)
			return nil
		}, func() {})
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- orch.Start(context.Background())
	}()

	// Phase 1 completes immediately (NoPrecondition)
	time.Sleep(20 * time.Millisecond)
	clock.Tick()
	time.Sleep(20 * time.Millisecond)

	got := record.get()
	if len(got) < 1 || got[0] != "proc-a" {
		t.Fatalf("expected proc-a started first, got %v", got)
	}
	if len(got) > 1 {
		t.Fatalf("proc-b should not have started yet, got %v", got)
	}

	// Satisfy phase 2
	preCond2.setSatisfied(true)
	clock.Tick()
	time.Sleep(30 * time.Millisecond)

	got = record.get()
	if len(got) < 2 || got[1] != "proc-b" {
		t.Fatalf("expected proc-b started second, got %v", got)
	}

	// Satisfy phase 3
	preCond3.setSatisfied(true)
	clock.Tick()
	time.Sleep(30 * time.Millisecond)

	got = record.get()
	if len(got) != 3 || got[2] != "proc-c" {
		t.Fatalf("expected proc-c third, got %v", got)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
}

func TestOrchestrator_StopReverseOrder(t *testing.T) {
	t.Parallel()
	var stopped []string
	var mu sync.Mutex

	plan := &StartupPlan{
		ClockType: testClockType,
		Phases: []StartupPhase{
			{Name: "p1", ProcessNames: []string{"a"}, Precondition: &NoPrecondition{}},
			{Name: "p2", ProcessNames: []string{"b"}, Precondition: &NoPrecondition{}},
			{Name: "p3", ProcessNames: []string{"c"}, Precondition: &NoPrecondition{}},
		},
		FailureSemantics:  map[string]ProcessFailureSemantics{},
		DefaultThresholds: map[string]time.Duration{},
	}

	orch := NewProcessOrchestrator(plan, WithPollInterval(1*time.Millisecond))

	for _, name := range []string{"a", "b", "c"} {
		n := name
		orch.RegisterProcess(n, func() error { return nil }, func() {
			mu.Lock()
			stopped = append(stopped, n)
			mu.Unlock()
		})
	}

	go func() {
		_ = orch.Start(context.Background())
	}()
	time.Sleep(50 * time.Millisecond)
	orch.Stop()

	mu.Lock()
	defer mu.Unlock()
	if len(stopped) != 3 {
		t.Fatalf("expected 3 stops, got %v", stopped)
	}
	expected := []string{"c", "b", "a"}
	for i, want := range expected {
		if stopped[i] != want {
			t.Errorf("stop order[%d] = %q, want %q", i, stopped[i], want)
		}
	}
}

func TestOrchestrator_ContextCancellation(t *testing.T) {
	t.Parallel()
	clock := newFakeClock(time.Now())
	blocked := &controllablePrecondition{satisfied: false}

	plan := &StartupPlan{
		ClockType: testClockType,
		Phases: []StartupPhase{
			{Name: "p1", ProcessNames: []string{"a"}, Precondition: blocked},
		},
		FailureSemantics:  map[string]ProcessFailureSemantics{},
		DefaultThresholds: map[string]time.Duration{},
	}

	orch := NewProcessOrchestrator(plan, WithClock(clock), WithPollInterval(10*time.Millisecond))
	orch.RegisterProcess("a", func() error { return nil }, func() {})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- orch.Start(ctx)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()
	clock.Tick()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error from cancelled context")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("orchestrator did not stop after context cancellation")
	}
}

func TestOrchestrator_RestartBypassesPrecondition(t *testing.T) {
	t.Parallel()
	orch := NewProcessOrchestrator(&StartupPlan{
		ClockType:         "test",
		Phases:            []StartupPhase{},
		FailureSemantics:  map[string]ProcessFailureSemantics{},
		DefaultThresholds: map[string]time.Duration{"proc-x": 5 * time.Second},
	})

	orch.RegisterProcess("proc-x", func() error { return nil }, func() {})

	now := time.Now()
	orch.OnProcessExit("proc-x", now)
	if orch.IsRunning("proc-x") {
		t.Fatal("process should not be running after exit")
	}

	result := orch.OnProcessRestart("proc-x", now.Add(2*time.Second))
	if !orch.IsRunning("proc-x") {
		t.Fatal("process should be running after restart")
	}
	if result.Downtime != 2*time.Second {
		t.Errorf("downtime = %v, want 2s", result.Downtime)
	}
}

func TestOrchestrator_OnProcessExitReturnsSemantics(t *testing.T) {
	t.Parallel()
	plan := &StartupPlan{
		ClockType: testClockType,
		Phases:    []StartupPhase{},
		FailureSemantics: map[string]ProcessFailureSemantics{
			orchPtp4lTR: {EnterHoldover: true, FlipSyncDirection: true},
		},
		DefaultThresholds: map[string]time.Duration{},
	}

	orch := NewProcessOrchestrator(plan)
	sem := orch.OnProcessExit(orchPtp4lTR, time.Now())
	if !sem.EnterHoldover || !sem.FlipSyncDirection {
		t.Errorf("unexpected semantics: %+v", sem)
	}

	unknown := orch.OnProcessExit("unknown", time.Now())
	if unknown.EnterHoldover || unknown.FlipSyncDirection {
		t.Errorf("unknown process should have zero semantics: %+v", unknown)
	}
}

func TestOrchestrator_IsRunningImplementsInterface(t *testing.T) {
	t.Parallel()
	orch := NewProcessOrchestrator(&StartupPlan{
		ClockType:         "test",
		Phases:            []StartupPhase{},
		FailureSemantics:  map[string]ProcessFailureSemantics{},
		DefaultThresholds: map[string]time.Duration{},
	})

	var provider ProcessStateProvider = orch
	if provider.IsRunning("any") {
		t.Fatal("should not be running")
	}
}

func TestOrchestrator_ParallelProcessesInPhase(t *testing.T) {
	t.Parallel()
	record := &startRecord{}

	plan := &StartupPlan{
		ClockType: testClockType,
		Phases: []StartupPhase{
			{
				Name:         "parallel",
				ProcessNames: []string{"a", "b", "c"},
				Precondition: &NoPrecondition{},
			},
		},
		FailureSemantics:  map[string]ProcessFailureSemantics{},
		DefaultThresholds: map[string]time.Duration{},
	}

	orch := NewProcessOrchestrator(plan, WithPollInterval(1*time.Millisecond))
	for _, name := range []string{"a", "b", "c"} {
		n := name
		orch.RegisterProcess(n, func() error {
			record.append(n)
			return nil
		}, func() {})
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- orch.Start(context.Background())
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Start() error: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Start() did not complete")
	}

	got := record.get()
	if len(got) != 3 {
		t.Fatalf("expected 3 processes started, got %d: %v", len(got), got)
	}
}

func TestOrchestrator_DoubleStartError(t *testing.T) {
	t.Parallel()
	plan := &StartupPlan{
		ClockType: testClockType,
		Phases: []StartupPhase{
			{Name: "p1", ProcessNames: []string{"a"}, Precondition: &NoPrecondition{}},
		},
		FailureSemantics:  map[string]ProcessFailureSemantics{},
		DefaultThresholds: map[string]time.Duration{},
	}
	orch := NewProcessOrchestrator(plan, WithPollInterval(1*time.Millisecond))
	orch.RegisterProcess("a", func() error { return nil }, func() {})

	go func() {
		_ = orch.Start(context.Background())
	}()
	time.Sleep(30 * time.Millisecond)

	if err := orch.Start(context.Background()); err == nil {
		t.Fatal("expected error on double start")
	}
}

func TestOrchestrator_StartFailure(t *testing.T) {
	t.Parallel()
	plan := &StartupPlan{
		ClockType: testClockType,
		Phases: []StartupPhase{
			{Name: "p1", ProcessNames: []string{"a"}, Precondition: &NoPrecondition{}},
		},
		FailureSemantics:  map[string]ProcessFailureSemantics{},
		DefaultThresholds: map[string]time.Duration{},
	}
	orch := NewProcessOrchestrator(plan, WithPollInterval(1*time.Millisecond))
	orch.RegisterProcess("a", func() error {
		return fmt.Errorf("simulated failure")
	}, func() {})

	err := orch.Start(context.Background())
	if err == nil {
		t.Fatal("expected error from failed process start")
	}
}
