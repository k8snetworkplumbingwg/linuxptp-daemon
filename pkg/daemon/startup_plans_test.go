package daemon

import (
	"testing"
	"time"
)

func TestStartupPlan_TBC(t *testing.T) {
	t.Parallel()
	ptpSource := &testPTPSourceState{}
	processState := &testProcessState{running: map[string]bool{}}
	bcObserver := &testBCStateObserver{}

	plan := NewTBCStartupPlan(ptpSource, processState, bcObserver, 100, 3)

	t.Run("clock type", func(t *testing.T) {
		t.Parallel()
		if plan.ClockType != "T-BC" {
			t.Errorf("ClockType = %q, want T-BC", plan.ClockType)
		}
	})

	t.Run("phase count", func(t *testing.T) {
		t.Parallel()
		if len(plan.Phases) != 4 {
			t.Fatalf("expected 4 phases, got %d", len(plan.Phases))
		}
	})

	t.Run("phase order", func(t *testing.T) {
		t.Parallel()
		expected := []struct {
			name  string
			procs []string
		}{
			{orchPtp4lTR + "-start", []string{orchPtp4lTR}},
			{orchPhasePhc2sys, []string{phc2sysProcessName}},
			{orchPhaseTs2phc, []string{ts2phcProcessName}},
			{orchPtp4lTT + "-start", []string{orchPtp4lTT}},
		}
		for i, exp := range expected {
			phase := plan.Phases[i]
			if phase.Name != exp.name {
				t.Errorf("phase[%d].Name = %q, want %q", i, phase.Name, exp.name)
			}
			if len(phase.ProcessNames) != len(exp.procs) {
				t.Errorf("phase[%d].ProcessNames = %v, want %v", i, phase.ProcessNames, exp.procs)
				continue
			}
			for j, p := range exp.procs {
				if phase.ProcessNames[j] != p {
					t.Errorf("phase[%d].ProcessNames[%d] = %q, want %q", i, j, phase.ProcessNames[j], p)
				}
			}
		}
	})

	t.Run("failure semantics", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			proc string
			want ProcessFailureSemantics
		}{
			{orchPtp4lTR, ProcessFailureSemantics{EnterHoldover: true, FlipSyncDirection: true, SuppressE3Always: true}},
			{orchPtp4lTT, ProcessFailureSemantics{EmitE1Always: true}},
			{ts2phcProcessName, ProcessFailureSemantics{SilentRestart: true}},
			{phc2sysProcessName, ProcessFailureSemantics{SilentRestart: true, SuppressE3Always: true}},
		}
		for _, tc := range tests {
			tc := tc
			t.Run(tc.proc, func(t *testing.T) {
				t.Parallel()
				got, exists := plan.FailureSemantics[tc.proc]
				if !exists {
					t.Fatalf("no failure semantics for %q", tc.proc)
				}
				if got != tc.want {
					t.Errorf("FailureSemantics[%q] = %+v, want %+v", tc.proc, got, tc.want)
				}
			})
		}
	})

	t.Run("default thresholds", func(t *testing.T) {
		t.Parallel()
		for _, proc := range []string{orchPtp4lTR, orchPtp4lTT, phc2sysProcessName, ts2phcProcessName} {
			if plan.DefaultThresholds[proc] != 5*time.Second {
				t.Errorf("DefaultThresholds[%q] = %v, want 5s", proc, plan.DefaultThresholds[proc])
			}
		}
	})

	t.Run("phase 1 has NoPrecondition", func(t *testing.T) {
		t.Parallel()
		if !plan.Phases[0].Precondition.Satisfied() {
			t.Error("phase 1 precondition should always be satisfied")
		}
	})
}

func TestStartupPlan_TGM(t *testing.T) {
	t.Parallel()
	ts2phcState := &testPTPSourceState{}
	processState := &testProcessState{running: map[string]bool{}}

	plan := NewTGMStartupPlan(ts2phcState, processState, 100, 3)

	t.Run("clock type", func(t *testing.T) {
		t.Parallel()
		if plan.ClockType != "T-GM" {
			t.Errorf("ClockType = %q, want T-GM", plan.ClockType)
		}
	})

	t.Run("phase count", func(t *testing.T) {
		t.Parallel()
		if len(plan.Phases) != 4 {
			t.Fatalf("expected 4 phases, got %d", len(plan.Phases))
		}
	})

	t.Run("phase order", func(t *testing.T) {
		t.Parallel()
		expected := []struct {
			name  string
			procs []string
		}{
			{orchPhaseTs2phc, []string{ts2phcProcessName}},
			{ptp4lProcessName + "-start", []string{ptp4lProcessName}},
			{orchPhasePhc2sys, []string{phc2sysProcessName}},
			{syncEProcessName + "-start", []string{syncEProcessName}},
		}
		for i, exp := range expected {
			phase := plan.Phases[i]
			if phase.Name != exp.name {
				t.Errorf("phase[%d].Name = %q, want %q", i, phase.Name, exp.name)
			}
			if len(phase.ProcessNames) != len(exp.procs) {
				t.Errorf("phase[%d].ProcessNames = %v, want %v", i, phase.ProcessNames, exp.procs)
				continue
			}
			for j, p := range exp.procs {
				if phase.ProcessNames[j] != p {
					t.Errorf("phase[%d].ProcessNames[%d] = %q, want %q", i, j, phase.ProcessNames[j], p)
				}
			}
		}
	})

	t.Run("failure semantics", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			proc string
			want ProcessFailureSemantics
		}{
			{ts2phcProcessName, ProcessFailureSemantics{EnterHoldover: true}},
			{ptp4lProcessName, ProcessFailureSemantics{EmitE1Always: true}},
			{phc2sysProcessName, ProcessFailureSemantics{SilentRestart: true, SuppressE3Always: true}},
			{syncEProcessName, ProcessFailureSemantics{SilentRestart: true}},
		}
		for _, tc := range tests {
			tc := tc
			t.Run(tc.proc, func(t *testing.T) {
				t.Parallel()
				got, exists := plan.FailureSemantics[tc.proc]
				if !exists {
					t.Fatalf("no failure semantics for %q", tc.proc)
				}
				if got != tc.want {
					t.Errorf("FailureSemantics[%q] = %+v, want %+v", tc.proc, got, tc.want)
				}
			})
		}
	})

	t.Run("default thresholds", func(t *testing.T) {
		t.Parallel()
		for _, proc := range []string{ts2phcProcessName, ptp4lProcessName, phc2sysProcessName, syncEProcessName} {
			if plan.DefaultThresholds[proc] != 5*time.Second {
				t.Errorf("DefaultThresholds[%q] = %v, want 5s", proc, plan.DefaultThresholds[proc])
			}
		}
	})

	t.Run("phase 1 ts2phc no precondition", func(t *testing.T) {
		t.Parallel()
		phase := plan.Phases[0]
		if len(phase.ProcessNames) != 1 || phase.ProcessNames[0] != ts2phcProcessName {
			t.Fatalf("phase 1 should be ts2phc, got %v", phase.ProcessNames)
		}
		if !phase.Precondition.Satisfied() {
			t.Error("phase 1 precondition should always be satisfied")
		}
	})
}
