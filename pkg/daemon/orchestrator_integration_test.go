package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegration_TBCStartupPlan_PhasedOrder(t *testing.T) {
	t.Parallel()

	sourceProvider := &mockPTPSourceState{state: event.PTP_LOCKED, offset: 10}
	processState := &mockProcessState{running: map[string]bool{}}
	bcObserver := &mockBCState{state: event.PTP_LOCKED}

	plan := NewTBCStartupPlan(sourceProvider, processState, bcObserver, 100, 3)
	orch := NewProcessOrchestrator(plan, WithPollInterval(5*time.Millisecond))

	var mu sync.Mutex
	var startOrder []string
	record := func(name string) func() error {
		return func() error {
			mu.Lock()
			startOrder = append(startOrder, name)
			mu.Unlock()
			processState.mu.Lock()
			processState.running[name] = true
			processState.mu.Unlock()
			return nil
		}
	}

	for _, name := range []string{orchPtp4lTR, phc2sysProcessName, ts2phcProcessName, orchPtp4lTT} {
		orch.RegisterProcess(name, record(name), func() {})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := orch.Start(ctx)
	require.NoError(t, err)

	mu.Lock()
	order := append([]string{}, startOrder...)
	mu.Unlock()

	assert.Equal(t, []string{orchPtp4lTR, phc2sysProcessName, ts2phcProcessName, orchPtp4lTT}, order,
		"T-BC processes must start in phased order")
}

func TestIntegration_TBCLifecycle_StartExitRestartStop(t *testing.T) {
	t.Parallel()

	sourceProvider := &mockPTPSourceState{state: event.PTP_LOCKED, offset: 10}
	processState := &mockProcessState{running: map[string]bool{}}
	bcObserver := &mockBCState{state: event.PTP_LOCKED}

	plan := NewTBCStartupPlan(sourceProvider, processState, bcObserver, 100, 3)
	orch := NewProcessOrchestrator(plan, WithPollInterval(5*time.Millisecond))

	var mu sync.Mutex
	startCount := map[string]int{}
	stopCount := map[string]int{}

	for _, name := range []string{orchPtp4lTR, phc2sysProcessName, ts2phcProcessName, orchPtp4lTT} {
		n := name
		orch.RegisterProcess(n,
			func() error {
				mu.Lock()
				startCount[n]++
				mu.Unlock()
				processState.mu.Lock()
				processState.running[n] = true
				processState.mu.Unlock()
				return nil
			},
			func() {
				mu.Lock()
				stopCount[n]++
				mu.Unlock()
				processState.mu.Lock()
				processState.running[n] = false
				processState.mu.Unlock()
			},
		)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := orch.Start(ctx)
	require.NoError(t, err)

	for _, name := range []string{orchPtp4lTR, phc2sysProcessName, ts2phcProcessName, orchPtp4lTT} {
		assert.True(t, orch.IsRunning(name), "%s should be running after start", name)
	}

	exitTime := time.Now()
	orch.OnProcessExit(phc2sysProcessName, exitTime)
	assert.False(t, orch.IsRunning(phc2sysProcessName))

	restartTime := exitTime.Add(2 * time.Second)
	result := orch.OnProcessRestart(phc2sysProcessName, restartTime)
	assert.True(t, orch.IsRunning(phc2sysProcessName), "phc2sys should be running after restart")
	assert.Equal(t, 2*time.Second, result.Downtime)
	assert.False(t, result.ThresholdExceeded)

	orch.Stop()

	for _, name := range []string{orchPtp4lTR, phc2sysProcessName, ts2phcProcessName, orchPtp4lTT} {
		assert.False(t, orch.IsRunning(name), "%s should be stopped", name)
	}

	mu.Lock()
	for _, name := range []string{orchPtp4lTR, phc2sysProcessName, ts2phcProcessName, orchPtp4lTT} {
		assert.Equal(t, 1, startCount[name], "%s start count", name)
		assert.Equal(t, 1, stopCount[name], "%s stop count", name)
	}
	mu.Unlock()
}

func TestIntegration_TBCPreconditionGating(t *testing.T) {
	t.Parallel()

	sourceProvider := &mockPTPSourceState{state: event.PTP_FREERUN, offset: 500}
	processState := &mockProcessState{running: map[string]bool{}}
	bcObserver := &mockBCState{state: event.PTP_FREERUN}

	plan := NewTBCStartupPlan(sourceProvider, processState, bcObserver, 100, 3)
	orch := NewProcessOrchestrator(plan, WithPollInterval(5*time.Millisecond))

	var mu sync.Mutex
	var startOrder []string
	for _, name := range []string{orchPtp4lTR, phc2sysProcessName, ts2phcProcessName, orchPtp4lTT} {
		n := name
		orch.RegisterProcess(n,
			func() error {
				mu.Lock()
				startOrder = append(startOrder, n)
				mu.Unlock()
				processState.mu.Lock()
				processState.running[n] = true
				processState.mu.Unlock()
				return nil
			},
			func() {},
		)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := orch.Start(ctx)
	assert.Error(t, err, "should timeout because PTP source is not qualified")

	mu.Lock()
	assert.Equal(t, []string{orchPtp4lTR}, startOrder,
		"only ptp4l-tr should start; phc2sys blocked by precondition")
	mu.Unlock()
}

func TestIntegration_DowntimeThresholdExceeded(t *testing.T) {
	t.Parallel()

	sourceProvider := &mockPTPSourceState{state: event.PTP_LOCKED, offset: 10}
	processState := &mockProcessState{running: map[string]bool{}}
	bcObserver := &mockBCState{state: event.PTP_LOCKED}

	plan := NewTBCStartupPlan(sourceProvider, processState, bcObserver, 100, 3)
	orch := NewProcessOrchestrator(plan, WithPollInterval(5*time.Millisecond))

	for _, name := range []string{orchPtp4lTR, phc2sysProcessName, ts2phcProcessName, orchPtp4lTT} {
		n := name
		orch.RegisterProcess(n,
			func() error {
				processState.mu.Lock()
				processState.running[n] = true
				processState.mu.Unlock()
				return nil
			},
			func() {},
		)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := orch.Start(ctx)
	require.NoError(t, err)

	exitTime := time.Now()
	orch.OnProcessExit(orchPtp4lTR, exitTime)

	restartTime := exitTime.Add(10 * time.Second)
	result := orch.OnProcessRestart(orchPtp4lTR, restartTime)

	assert.True(t, result.ThresholdExceeded,
		"10s downtime should exceed the 5s default threshold")
	assert.Equal(t, 10*time.Second, result.Downtime)
}

func TestIntegration_EventSuppressionWithOrchestrator(t *testing.T) {
	t.Parallel()

	sourceProvider := &mockPTPSourceState{state: event.PTP_LOCKED, offset: 10}
	processState := &mockProcessState{running: map[string]bool{}}
	bcObserver := &mockBCState{state: event.PTP_LOCKED}

	plan := NewTBCStartupPlan(sourceProvider, processState, bcObserver, 100, 3)
	orch := NewProcessOrchestrator(plan, WithPollInterval(5*time.Millisecond))

	gate := NewEventSuppressionGate(orch.DowntimeTracker(), plan.FailureSemantics)

	assert.True(t, gate.ShouldSuppressEvent(orchPtp4lTR, EventTypeOSClockSyncState),
		"ptp4l-tr should always suppress E3 per failure semantics")

	assert.False(t, gate.ShouldSuppressEvent(orchPtp4lTT, EventTypePTPStateChange),
		"ptp4l-tt should never suppress E1 (EmitE1Always)")

	assert.False(t, gate.ShouldSuppressEvent(phc2sysProcessName, EventTypePTPStateChange),
		"phc2sys should not suppress E1 when not in suppressed state")

	gate.SetSuppressed(phc2sysProcessName, true)
	assert.True(t, gate.ShouldSuppressEvent(phc2sysProcessName, EventTypePTPStateChange),
		"phc2sys should suppress events when in silent restart suppression")
}

// --- Mock implementations for integration tests ---

type mockPTPSourceState struct {
	mu     sync.Mutex
	state  event.PTPState
	offset float64
}

func (m *mockPTPSourceState) ServoState() event.PTPState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

func (m *mockPTPSourceState) CurrentOffset() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.offset
}

type mockProcessState struct {
	mu      sync.Mutex
	running map[string]bool
}

func (m *mockProcessState) IsRunning(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running[name]
}

type mockBCState struct {
	mu    sync.Mutex
	state event.PTPState
}

func (m *mockBCState) CurrentState() event.PTPState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}
