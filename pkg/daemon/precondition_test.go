package daemon

import (
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
)

type testPTPSourceState struct {
	servo  event.PTPState
	offset float64
}

func (m *testPTPSourceState) ServoState() event.PTPState { return m.servo }
func (m *testPTPSourceState) CurrentOffset() float64     { return m.offset }

type testProcessState struct {
	running map[string]bool
}

func (m *testProcessState) IsRunning(name string) bool { return m.running[name] }

type testBCStateObserver struct {
	state event.PTPState
}

func (m *testBCStateObserver) CurrentState() event.PTPState { return m.state }

func TestPrecondition_NoPrecondition(t *testing.T) {
	t.Parallel()
	p := &NoPrecondition{}
	if !p.Satisfied() {
		t.Fatal("NoPrecondition should always be satisfied")
	}
	if p.Name() != "none" {
		t.Fatalf("expected name 'none', got %q", p.Name())
	}
}

func TestPrecondition_PTPSourceQualified(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		servo           event.PTPState
		offsets         []float64
		threshold       float64
		requiredSamples int
		wantSatisfied   bool
	}{
		{
			name:            "not locked",
			servo:           event.PTP_FREERUN,
			offsets:         []float64{10, 10, 10},
			threshold:       100,
			requiredSamples: 3,
			wantSatisfied:   false,
		},
		{
			name:            "locked but offset exceeds threshold",
			servo:           event.PTP_LOCKED,
			offsets:         []float64{200, 200, 200},
			threshold:       100,
			requiredSamples: 3,
			wantSatisfied:   false,
		},
		{
			name:            "locked, offset ok, enough samples",
			servo:           event.PTP_LOCKED,
			offsets:         []float64{50, 50, 50},
			threshold:       100,
			requiredSamples: 3,
			wantSatisfied:   true,
		},
		{
			name:            "locked, offset ok, insufficient samples",
			servo:           event.PTP_LOCKED,
			offsets:         []float64{50, 50},
			threshold:       100,
			requiredSamples: 3,
			wantSatisfied:   false,
		},
		{
			name:            "negative offset within threshold",
			servo:           event.PTP_LOCKED,
			offsets:         []float64{-50, -50, -50},
			threshold:       100,
			requiredSamples: 3,
			wantSatisfied:   true,
		},
		{
			name:            "offset crosses threshold mid-sequence resets count",
			servo:           event.PTP_LOCKED,
			offsets:         []float64{50, 50, 200, 50, 50, 50},
			threshold:       100,
			requiredSamples: 3,
			wantSatisfied:   true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			provider := &testPTPSourceState{servo: tc.servo}
			p := NewPTPSourceQualifiedPrecondition(provider, tc.threshold, tc.requiredSamples)

			var result bool
			for _, offset := range tc.offsets {
				provider.offset = offset
				result = p.Satisfied()
			}
			if result != tc.wantSatisfied {
				t.Errorf("Satisfied() = %v, want %v", result, tc.wantSatisfied)
			}
		})
	}
}

func TestPrecondition_PTPSourceQualified_ResetCount(t *testing.T) {
	t.Parallel()
	provider := &testPTPSourceState{servo: event.PTP_LOCKED, offset: 10}
	p := NewPTPSourceQualifiedPrecondition(provider, 100, 3)

	p.Satisfied()
	p.Satisfied()
	p.ResetCount()
	if p.Satisfied() {
		t.Fatal("should not be satisfied after reset with only 1 sample")
	}
}

func TestPrecondition_ProcessRunning(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		process string
		running map[string]bool
		want    bool
	}{
		{
			name:    "process is running",
			process: phc2sysProcessName,
			running: map[string]bool{phc2sysProcessName: true},
			want:    true,
		},
		{
			name:    "process is not running",
			process: phc2sysProcessName,
			running: map[string]bool{phc2sysProcessName: false},
			want:    false,
		},
		{
			name:    "process not in map",
			process: ts2phcProcessName,
			running: map[string]bool{},
			want:    false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			provider := &testProcessState{running: tc.running}
			p := NewProcessRunningPrecondition(tc.process, provider)
			if got := p.Satisfied(); got != tc.want {
				t.Errorf("Satisfied() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPrecondition_BCState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		state event.PTPState
		want  bool
	}{
		{"locked satisfies", event.PTP_LOCKED, true},
		{"freerun does not satisfy", event.PTP_FREERUN, false},
		{"holdover does not satisfy", event.PTP_HOLDOVER, false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			obs := &testBCStateObserver{state: tc.state}
			p := NewBCStatePrecondition(obs)
			if got := p.Satisfied(); got != tc.want {
				t.Errorf("Satisfied() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPrecondition_Composite(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		preconditions []StartupPrecondition
		wantSatisfied bool
	}{
		{
			name:          "all satisfied",
			preconditions: []StartupPrecondition{&NoPrecondition{}, &NoPrecondition{}},
			wantSatisfied: true,
		},
		{
			name: "one unsatisfied",
			preconditions: []StartupPrecondition{
				&NoPrecondition{},
				NewProcessRunningPrecondition("x", &testProcessState{running: map[string]bool{}}),
			},
			wantSatisfied: false,
		},
		{
			name:          "empty composite",
			preconditions: []StartupPrecondition{},
			wantSatisfied: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := NewCompositePrecondition(tc.preconditions...)
			if got := p.Satisfied(); got != tc.wantSatisfied {
				t.Errorf("Satisfied() = %v, want %v", got, tc.wantSatisfied)
			}
		})
	}
}

func TestPrecondition_Timed(t *testing.T) {
	t.Parallel()

	t.Run("inner satisfied before timeout", func(t *testing.T) {
		t.Parallel()
		inner := &NoPrecondition{}
		p := NewTimedPrecondition(inner, 1*time.Second)
		if !p.Satisfied() {
			t.Fatal("should be satisfied immediately when inner is satisfied")
		}
		if p.TimedOut() {
			t.Fatal("should not be timed out")
		}
	})

	t.Run("timeout triggers when inner unsatisfied", func(t *testing.T) {
		t.Parallel()
		provider := &testProcessState{running: map[string]bool{}}
		inner := NewProcessRunningPrecondition("x", provider)
		p := NewTimedPrecondition(inner, 10*time.Millisecond)

		if p.Satisfied() {
			t.Fatal("should not be satisfied initially")
		}
		time.Sleep(20 * time.Millisecond)
		if !p.Satisfied() {
			t.Fatal("should be satisfied after timeout")
		}
		if !p.TimedOut() {
			t.Fatal("should report timed out")
		}
	})
}
