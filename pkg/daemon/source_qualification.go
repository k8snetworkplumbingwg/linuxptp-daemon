package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/golang/glog"
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
	mu           sync.Mutex // teardown waits for an in-flight hardware action
	closed       bool
	generation   uint64
	ctx          context.Context
	done         chan struct{}
	apply        func(bool) error
	desired      event.SourceQualification
	desiredKnown bool
	applied      bool
	appliedKnown bool
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
	p.sourceQualification = &sourceQualificationRuntime{
		generation: generation,
		done:       make(chan struct{}),
		apply: func(ready bool) error {
			condition := hardwareconfig.ConditionTypeLost
			if ready {
				condition = hardwareconfig.ConditionTypeLocked
			}
			if hasHardware {
				return p.dn.hardwareConfigManager.ApplyConditionForProfile(p.nodeProfile, condition)
			}
			hook := "tbc-ho-entry"
			if ready {
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
// clock evaluates readiness; input actions are serialized here.
func (p *ptpProcess) forwardSourceData(metrics *parser.Metrics, portEvent *parser.PTPEvent) {
	q := p.sourceQualification
	if q == nil || p.name != event.Ptp4lProcessName {
		return
	}
	if portEvent != nil && portEvent.BMCASelection != parserconstants.BMCAUnknown {
		if portEvent.ConfigName != p.configName {
			return // Only the TR instance's own BMCA result controls its source.
		}
		clockID, err := p.getPTPClockID()
		if err != nil {
			glog.Error(err)
		}
		p.observeSource("", &event.PTPSourceObservation{Selection: portEvent.BMCASelection, ClockID: clockID})
		return
	}
	if portEvent != nil && portEvent.Role != parserconstants.PortRoleUnknown {
		port := portEvent.Iface
		if port == "" && portEvent.PortID > 0 && portEvent.PortID <= len(p.ifaces) {
			port = p.ifaces[portEvent.PortID-1].Name
		}
		if portEvent.Role == parserconstants.PortRoleSlave && slices.Contains(p.tBCAttributes.trIfaceNames, port) {
			p.tBCAttributes.activePort = port
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
	q := p.sourceQualification
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	observation.Generation = q.generation
	ctx := q.ctx
	q.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	observation.Reply = make(chan event.SourceQualification, 1)
	ev := event.Event{Source: event.PTP4l, CfgName: p.configName, IFace: port, ClockType: p.clockType, Time: time.Now().UnixMilli(), Data: observation}
	if observation.Selection != parserconstants.BMCAUnknown || observation.PortRole != nil || observation.SourceLost {
		// These transitions may not repeat. Wait for delivery, but allow daemon
		// shutdown to release a sender whose consumer has stopped.
		select {
		case p.eventCh <- ev:
		case <-ctx.Done():
			return
		case <-q.done:
			return
		}
	} else {
		select {
		case p.eventCh <- ev:
		case <-ctx.Done():
			return
		case <-q.done:
			return
		default:
			glog.Errorf("source qualification event channel full for %s[%s]; dropping observation", p.name, p.configName)
			return
		}
	}
	select {
	case result := <-observation.Reply:
		if result.SourceType == event.PTP4l {
			p.applySourceQualification(result)
		}
	case <-ctx.Done():
	case <-q.done:
	}
}

func (p *ptpProcess) applySourceQualification(result event.SourceQualification) {
	q := p.sourceQualification
	q.mu.Lock()
	if q.closed || result.Generation != q.generation || (q.desiredKnown && result.Revision < q.desired.Revision) {
		q.mu.Unlock()
		return
	}
	// Compare desired states so a failed action is not repeated by later samples.
	inputChanged := !q.desiredKnown || q.desired.Ready != result.Ready
	q.desired, q.desiredKnown = result, true
	p.tBCAttributes.activePort = result.IFace // label existing state/averaged-offset events
	if result.Changed {
		glog.Infof("PTPSourceQualified profile=%s config=%s source=%s port=%s qualified=%t reason=%q samples=%d",
			getProfileName(p.nodeProfile), p.configName, result.SourceType, result.IFace, result.Qualified, result.Reason, result.Samples)
	}
	ctx := q.ctx
	var ack *event.PTPSourceApplied
	if inputChanged && (ctx == nil || ctx.Err() == nil) {
		if err := q.apply(result.Ready); err != nil {
			q.appliedKnown = false // A failed write may have partially programmed pins.
			glog.Errorf("PTPSourceQualified hardware apply profile=%s ready=%t: %v", getProfileName(p.nodeProfile), result.Ready, err)
		} else {
			q.applied, q.appliedKnown = result.Ready, true
			if result.Ready {
				ack = &event.PTPSourceApplied{Generation: result.Generation, Revision: result.Revision, Enabled: true}
			}
		}
	}
	q.mu.Unlock()
	p.sendSourceInputApplied(ctx, ack)
}

func (p *ptpProcess) sendSourceInputApplied(ctx context.Context, ack *event.PTPSourceApplied) {
	if ack == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Action acknowledgements, like BMCA decisions, are state-bearing facts.
	// Never drop them or hold q.mu while waiting for the event forwarder.
	select {
	case p.eventCh <- event.Event{Source: event.PTP4l, CfgName: p.configName, ClockType: p.clockType, Time: time.Now().UnixMilli(), Data: ack}:
	case <-ctx.Done():
	case <-p.sourceQualification.done:
	}
}

func (p *ptpProcess) invalidateSourceQualification() {
	if p.sourceQualification == nil {
		return
	}
	p.observeSource("", &event.PTPSourceObservation{SourceLost: true})
}

func (q *sourceQualificationRuntime) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	if q.done != nil {
		close(q.done)
	}
}

func sourceQualifiedStartCondition(env ptpProcessEnv) process.Condition {
	return process.OnSourceQualified{ConfigName: getPtp4lConfig(env.runID)}
}

// resolveSourceQualificationPolicy always resolves a policy for a T-BC TR profile.
// Optional source settings override defaults; their presence never enables or
// disables qualification. TT-only and HA system-clock profiles have no TR input.
// Resolve threshold fallbacks once, independently of aggregate InSync at runtime.
func resolveSourceQualificationPolicy(profile *ptpv1.PtpProfile) (sourceQualificationPolicy, error) {
	if profileClockType(profile) != TBC {
		return sourceQualificationPolicy{}, nil
	}
	if err := validateTBCNoHAProfiles(profile); err != nil {
		return sourceQualificationPolicy{}, err
	}
	if profile.PtpSettings["controllingProfile"] != "" {
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
