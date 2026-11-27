package daemon

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/hardwareconfig"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	parserconstants "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

type sourceQualificationPolicy struct {
	applicable bool // Only the T-BC TR profile owns an upstream qualification policy.
	config     utils.QualificationConfig
}

type sourceQualificationRuntime struct {
	mu         sync.Mutex
	closed     bool
	generation uint64
	clockID    string
	ctx        context.Context
	apply      func(bool) error
	actions    *clock.SourceInputActions
}

func (p *ptpProcess) configureSourceQualification(policy sourceQualificationPolicy) error {
	if len(p.tBCAttributes.trIfaceNames) == 0 {
		return fmt.Errorf("source qualification requires upstream TR ports")
	}
	hasHardware := p.dn != nil && p.dn.hardwareConfigManager != nil && p.dn.hardwareConfigManager.HasHardwareConfigForProfile(p.nodeProfile)
	if policy.config.UseS3 && !hasHardware {
		return fmt.Errorf("S3 source qualification requires HardwareConfig")
	}
	if p.dn == nil || p.dn.processManager == nil || p.dn.processManager.clockMgr == nil {
		return fmt.Errorf("source qualification requires a clock manager")
	}
	generation, err := p.dn.processManager.clockMgr.ConfigureSourceQualification(p.configName, p.tBCAttributes.trIfaceNames, policy.config)
	if err != nil {
		return err
	}
	clockID := ""
	if p.nodeProfile != nil {
		var clockIDErr error
		clockID, clockIDErr = p.getPTPClockID()
		if clockIDErr != nil {
			glog.Error(clockIDErr)
		}
	}
	qlf := &sourceQualificationRuntime{
		generation: generation,
		clockID:    clockID,
		apply: func(qualified bool) error {
			condition := hardwareconfig.ConditionTypeLost
			if qualified {
				condition = hardwareconfig.ConditionTypeLocked
			}
			if hasHardware {
				return p.dn.hardwareConfigManager.ApplyConditionForProfile(p.nodeProfile, condition)
			}
			hook := "tbc-ho-entry"
			if qualified {
				hook = "tbc-ho-exit"
			}
			// The legacy dispatcher discards errors; collect them without skipping plugins.
			var failures []error
			if p.pm == nil {
				return nil
			}
			for name, handler := range p.pm.Plugins {
				if handler.AfterRunPTPCommand != nil {
					if hookErr := handler.AfterRunPTPCommand(p.pm.Data[name], p.nodeProfile, hook); hookErr != nil {
						failures = append(failures, fmt.Errorf("plugin %s %s: %w", name, hook, hookErr))
					}
				}
			}
			return errors.Join(failures...)
		},
	}
	p.sourceQualification = qlf
	actions, err := p.dn.processManager.clockMgr.SetSourceInputAction(p.configName, generation, getProfileName(p.nodeProfile), func(qualified bool) error {
		// The action worker and process startup/teardown share the runtime state.
		qlf.mu.Lock()
		apply := qlf.apply
		ctx := qlf.ctx
		closed := qlf.closed
		qlf.mu.Unlock()
		if apply == nil || (qualified && (closed || (ctx != nil && ctx.Err() != nil))) {
			return nil
		}
		return apply(qualified)
	})
	if err != nil {
		qlf.close()
		p.sourceQualification = nil
		return err
	}
	// Process setup and teardown share the action-worker handle.
	qlf.mu.Lock()
	qlf.actions = actions
	qlf.mu.Unlock()
	th := getPTPThreshold(p.nodeProfile)
	glog.Infof("Source qualification profile=%s config=%s S3=%t thresholds=%d/%d samples=%d/%d thresholdFallbacks=%t/%t",
		getProfileName(p.nodeProfile), p.configName, policy.config.UseS3,
		policy.config.QualifiedThreshold, policy.config.DisqualifiedThreshold,
		policy.config.QualifiedSamples, policy.config.DisqualifiedSamples,
		th.PtpSourceQualifiedThreshold == nil,
		th.PtpSourceDisqualifiedThreshold == nil)
	return nil
}

// forwardSourceData forwards fresh parser facts before metrics mutate them. The
// clock evaluates readiness and controls input actions.
func (p *ptpProcess) forwardSourceData(metrics *parser.Metrics, portEvent *parser.PTPEvent) {
	qlf := p.sourceQualification
	if qlf == nil || p.name != event.Ptp4lProcessName {
		return
	}
	if portEvent != nil && portEvent.Role != parserconstants.PortRoleUnknown {
		port := portEvent.Iface
		if port == "" && portEvent.PortID > 0 && portEvent.PortID <= len(p.ifaces) {
			port = p.ifaces[portEvent.PortID-1].Name
		}
		p.observeSource(port, &event.PTPSourceObservation{PortRole: utils.ToPtr(portEvent.Role)})
	}
	// Summary/port-status lines are not fresh servo measurements.
	if metrics == nil || metrics.ServoState == "" {
		return
	}
	p.observeSource(metrics.Iface, &event.PTPSourceObservation{Offset: metrics.Offset, ServoState: metrics.ServoState})
}

func (p *ptpProcess) observeSource(port string, observation *event.PTPSourceObservation) {
	qlf := p.sourceQualification
	// Scanner/process-exit forwarding and teardown share the publisher state.
	qlf.mu.Lock()
	if qlf.closed || (qlf.ctx != nil && qlf.ctx.Err() != nil) {
		qlf.mu.Unlock()
		return
	}
	observation.Generation = qlf.generation
	observation.ClockID = qlf.clockID
	ev := event.Event{Source: event.PTP4l, CfgName: p.configName, IFace: port, ClockType: p.clockType, Time: time.Now().UnixMilli(), Data: observation}
	select {
	case p.eventCh <- ev:
		qlf.mu.Unlock()
		return
	default:
		qlf.mu.Unlock()
		glog.Errorf("SOURCE EVENT CHANNEL FULL for %s[%s]: DROPPING event %s", p.name, p.configName, observation)
	}
}

func (p *ptpProcess) invalidateSourceQualification() {
	if p.sourceQualification == nil {
		return
	}
	p.observeSource("", &event.PTPSourceObservation{SourceLost: true})
}

func (qlf *sourceQualificationRuntime) close() {
	// Teardown, the scanner, and the action worker share the runtime state.
	qlf.mu.Lock()
	qlf.closed = true
	actions := qlf.actions
	generation := qlf.generation
	qlf.mu.Unlock()
	if actions != nil {
		actions.CloseGeneration(generation)
	}
}

func sourceQualifiedStartCondition(env ptpProcessEnv) process.Condition {
	var conditions []process.Condition
	if env.topology != nil && env.topology.isHAProfile(getProfileName(env.nodeProfile)) {
		for _, cfg := range haPtp4lConfigs(env.topology) {
			conditions = append(conditions, process.OnSourceQualified{ConfigName: cfg})
		}
	}
	if len(conditions) == 0 {
		return process.OnSourceQualified{ConfigName: getPtp4lConfig(env.runID)}
	}
	return process.Any{Conditions: conditions}
}

// resolveSourceQualificationPolicy always resolves a policy for a T-BC TR profile.
// Optional source settings override defaults; their presence never enables or
// disables qualification. TT-only and HA system-clock profiles have no TR input.
// Resolve threshold fallbacks once, independently of aggregate InSync at runtime.
func resolveSourceQualificationPolicy(profile *ptpv1.PtpProfile) (sourceQualificationPolicy, error) {
	if profileClockType(profile) != TBC || profile.PtpSettings["controllingProfile"] != "" || len(listHAMemberProfiles(profile)) > 0 {
		return sourceQualificationPolicy{}, nil
	}
	th := getPTPThreshold(profile)
	fallback := th.MaxOffsetThreshold
	if th.PtpSourceQualifiedThreshold == nil || th.PtpSourceDisqualifiedThreshold == nil {
		if fallback <= 0 {
			fallback = 100
			if isNtpFailoverEnabled(profile) {
				fallback = 1000
			}
		}
		if value, ok := profile.PtpSettings["inSyncConditionThreshold"]; ok {
			var err error
			fallback, err = strconv.ParseInt(value, 0, 64)
			if err != nil || fallback <= 0 {
				return sourceQualificationPolicy{}, fmt.Errorf("invalid source qualification fallback inSyncConditionThreshold %q", value)
			}
		}
	}
	cfg := utils.QualificationConfig{
		QualifiedThreshold: fallback, DisqualifiedThreshold: fallback,
		QualifiedSamples: 5, DisqualifiedSamples: 5, UseS3: th.PtpSourceUseS3,
	}
	if th.PtpSourceQualifiedThreshold != nil {
		cfg.QualifiedThreshold = *th.PtpSourceQualifiedThreshold
	}
	if th.PtpSourceDisqualifiedThreshold != nil {
		cfg.DisqualifiedThreshold = *th.PtpSourceDisqualifiedThreshold
	}
	for _, count := range []struct {
		value *int64
		dest  *int
	}{
		{th.PtpSourceQualifiedSamples, &cfg.QualifiedSamples},
		{th.PtpSourceDisqualifiedSamples, &cfg.DisqualifiedSamples},
	} {
		if count.value != nil {
			if *count.value < 1 || uint64(*count.value) > uint64(^uint(0)>>1) {
				return sourceQualificationPolicy{}, fmt.Errorf("invalid source qualification sample count %d", *count.value)
			}
			*count.dest = int(*count.value)
		}
	}
	if err := cfg.Validate(); err != nil {
		return sourceQualificationPolicy{}, fmt.Errorf("invalid source qualification policy: %w", err)
	}
	return sourceQualificationPolicy{applicable: true, config: cfg}, nil
}
