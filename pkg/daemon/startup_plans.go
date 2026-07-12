package daemon

import "time"

const (
	orchPtp4lTR      = "ptp4l-tr"
	orchPtp4lTT      = "ptp4l-tt"
	orchPhasePhc2sys = "phc2sys-start"
	orchPhaseTs2phc  = "ts2phc-start"
)

// NewTBCStartupPlan returns the startup plan for Telecom Boundary Clock mode.
// T-BC order: ptp4l[TR] → phc2sys → ts2phc → ptp4l[TT]
func NewTBCStartupPlan(
	ptpSourceProvider PTPSourceStateProvider,
	processStateProvider ProcessStateProvider,
	bcStateObserver BCStateObserver,
	offsetThreshold float64,
	requiredSamples int,
) *StartupPlan {
	return &StartupPlan{
		ClockType: "T-BC",
		Phases: []StartupPhase{
			{
				Name:         orchPtp4lTR + "-start",
				ProcessNames: []string{orchPtp4lTR},
				Precondition: &NoPrecondition{},
			},
			{
				Name:         orchPhasePhc2sys,
				ProcessNames: []string{phc2sysProcessName},
				Precondition: NewPTPSourceQualifiedPrecondition(ptpSourceProvider, offsetThreshold, requiredSamples),
			},
			{
				Name:         orchPhaseTs2phc,
				ProcessNames: []string{ts2phcProcessName},
				Precondition: NewCompositePrecondition(
					NewPTPSourceQualifiedPrecondition(ptpSourceProvider, offsetThreshold, requiredSamples),
					NewProcessRunningPrecondition(phc2sysProcessName, processStateProvider),
				),
			},
			{
				Name:         orchPtp4lTT + "-start",
				ProcessNames: []string{orchPtp4lTT},
				Precondition: NewBCStatePrecondition(bcStateObserver),
			},
		},
		FailureSemantics: map[string]ProcessFailureSemantics{
			orchPtp4lTR: {
				EnterHoldover:     true,
				FlipSyncDirection: true,
				SuppressE3Always:  true,
			},
			orchPtp4lTT: {
				EmitE1Always: true,
			},
			ts2phcProcessName: {
				SilentRestart: true,
			},
			phc2sysProcessName: {
				SilentRestart:    true,
				SuppressE3Always: true,
			},
		},
		DefaultThresholds: map[string]time.Duration{
			orchPtp4lTR:        5 * time.Second,
			orchPtp4lTT:        5 * time.Second,
			phc2sysProcessName: 5 * time.Second,
			ts2phcProcessName:  5 * time.Second,
		},
	}
}

// NewTGMStartupPlan returns the startup plan for Telecom Grandmaster mode.
// T-GM order: ts2phc → ptp4l → phc2sys → synce4l
// GNSS monitor (gpsd/gpspipe) and DPLL monitors are depProcesses of ts2phc,
// started automatically by the existing depProcess mechanism.
func NewTGMStartupPlan(
	ts2phcStateProvider PTPSourceStateProvider,
	processStateProvider ProcessStateProvider,
	offsetThreshold float64,
	requiredSamples int,
) *StartupPlan {
	return &StartupPlan{
		ClockType: "T-GM",
		Phases: []StartupPhase{
			{
				Name:         orchPhaseTs2phc,
				ProcessNames: []string{ts2phcProcessName},
				Precondition: &NoPrecondition{},
			},
			{
				Name:         ptp4lProcessName + "-start",
				ProcessNames: []string{ptp4lProcessName},
				Precondition: NewProcessRunningPrecondition(ts2phcProcessName, processStateProvider),
			},
			{
				Name:         orchPhasePhc2sys,
				ProcessNames: []string{phc2sysProcessName},
				Precondition: NewPTPSourceQualifiedPrecondition(ts2phcStateProvider, offsetThreshold, requiredSamples),
			},
			{
				Name:         syncEProcessName + "-start",
				ProcessNames: []string{syncEProcessName},
				Precondition: &NoPrecondition{},
			},
		},
		FailureSemantics: map[string]ProcessFailureSemantics{
			ts2phcProcessName: {
				EnterHoldover: true,
			},
			ptp4lProcessName: {
				EmitE1Always: true,
			},
			phc2sysProcessName: {
				SilentRestart:    true,
				SuppressE3Always: true,
			},
			syncEProcessName: {
				SilentRestart: true,
			},
		},
		DefaultThresholds: map[string]time.Duration{
			ts2phcProcessName:  5 * time.Second,
			ptp4lProcessName:   5 * time.Second,
			phc2sysProcessName: 5 * time.Second,
			syncEProcessName:   5 * time.Second,
		},
	}
}
