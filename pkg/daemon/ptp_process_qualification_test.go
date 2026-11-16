package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clockmgr"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	parserconstants "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/require"
)

func TestSourceQualificationPolicy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		profile string
		enabled bool
		want    utils.QualificationConfig
		wantErr bool
	}{
		{name: "minimal TBC uses defaults", profile: `{"ptpSettings":{"clockType":"T-BC"}}`, enabled: true,
			want: utils.QualificationConfig{QualifiedThreshold: 10, DisqualifiedThreshold: 1500, QualifiedSamples: 5, DisqualifiedSamples: 5}},
		{name: "explicit false UseS3 uses defaults", profile: `{"ptpSettings":{"clockType":"T-BC"},"ptpClockThreshold":{"ptpSourceUseS3":false}}`, enabled: true,
			want: utils.QualificationConfig{QualifiedThreshold: 10, DisqualifiedThreshold: 1500, QualifiedSamples: 5, DisqualifiedSamples: 5}},
		{name: "non TBC", profile: `{"ptpClockThreshold":{"ptpSourceQualifiedSamples":1}}`},
		{name: "samples override only", profile: `{"ptpSettings":{"clockType":"T-BC"},"ptpClockThreshold":{"ptpSourceQualifiedSamples":2}}`, enabled: true,
			want: utils.QualificationConfig{QualifiedThreshold: 10, DisqualifiedThreshold: 1500, QualifiedSamples: 2, DisqualifiedSamples: 5}},
		{name: "inSyncConditionThreshold is ignored", profile: `{"ptpSettings":{"clockType":"T-BC","inSyncConditionThreshold":"500"},"ptpClockThreshold":{"ptpSourceQualifiedSamples":1}}`, enabled: true,
			want: utils.QualificationConfig{QualifiedThreshold: 10, DisqualifiedThreshold: 1500, QualifiedSamples: 1, DisqualifiedSamples: 5}},
		{name: "partial pair", profile: `{"ptpSettings":{"clockType":"T-BC"},"ptpClockThreshold":{"maxOffsetThreshold":200,"ptpSourceQualifiedThreshold":50}}`, enabled: true,
			want: utils.QualificationConfig{QualifiedThreshold: 50, DisqualifiedThreshold: 1500, QualifiedSamples: 5, DisqualifiedSamples: 5}},
		{name: "qualified only", profile: `{"ptpSettings":{"clockType":"T-BC"},"ptpClockThreshold":{"ptpSourceQualifiedThreshold":500}}`, enabled: true,
			want: utils.QualificationConfig{QualifiedThreshold: 500, DisqualifiedThreshold: 1500, QualifiedSamples: 5, DisqualifiedSamples: 5}},
		{name: "explicit source values", profile: `{"ptpSettings":{"clockType":"T-BC","inSyncConditionThreshold":"invalid"},"ptpClockThreshold":{"ptpSourceQualifiedThreshold":20,"ptpSourceDisqualifiedThreshold":40,"ptpSourceQualifiedSamples":2,"ptpSourceDisqualifiedSamples":3}}`, enabled: true,
			want: utils.QualificationConfig{QualifiedThreshold: 20, DisqualifiedThreshold: 40, QualifiedSamples: 2, DisqualifiedSamples: 3}},
		{name: "S3 opt in", profile: `{"ptpSettings":{"clockType":"T-BC"},"ptpClockThreshold":{"ptpSourceUseS3":true}}`, enabled: true,
			want: utils.QualificationConfig{UseS3: true, QualifiedThreshold: 10, DisqualifiedThreshold: 1500, QualifiedSamples: 5, DisqualifiedSamples: 5}},
		{name: "S3 with NTP", profile: `{"ptpSettings":{"clockType":"T-BC"},"plugins":{"ntpfailover":{"gnssFailover":true}},"ptpClockThreshold":{"ptpSourceUseS3":true}}`, enabled: true,
			want: utils.QualificationConfig{UseS3: true, QualifiedThreshold: 10, DisqualifiedThreshold: 1500, QualifiedSamples: 5, DisqualifiedSamples: 5}},
		{name: "zero threshold", profile: `{"ptpSettings":{"clockType":"T-BC"},"ptpClockThreshold":{"ptpSourceQualifiedThreshold":0}}`, wantErr: true},
		{name: "negative count", profile: `{"ptpSettings":{"clockType":"T-BC"},"ptpClockThreshold":{"ptpSourceDisqualifiedSamples":-1}}`, wantErr: true},
		{name: "invalid inSyncConditionThreshold is ignored", profile: `{"ptpSettings":{"clockType":"T-BC","inSyncConditionThreshold":"NaN"},"ptpClockThreshold":{"ptpSourceQualifiedSamples":1}}`, enabled: true,
			want: utils.QualificationConfig{QualifiedThreshold: 10, DisqualifiedThreshold: 1500, QualifiedSamples: 1, DisqualifiedSamples: 5}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var p ptpv1.PtpProfile
			require.NoError(t, json.Unmarshal([]byte(tc.profile), &p))
			policy, err := resolveSourceQualificationPolicy(&p)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.enabled, policy.enabled)
			if tc.enabled {
				require.Equal(t, tc.want, policy.config)
			}
		})
	}
}

func TestSourceQualificationFreshSamplesAndHardware(t *testing.T) {
	t.Parallel()
	for _, useS3 := range []bool{false, true} {
		name := "offset"
		if useS3 {
			name = "s3"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var actions []bool
			p := &ptpProcess{
				ExecProcess:   ExecProcess{name: event.Ptp4lProcessName, configName: "ptp4l.0.config", eventCh: make(chan event.Event, 20)},
				nodeProfile:   &ptpv1.PtpProfile{},
				tBCAttributes: tBCProcessAttributes{trIfaceNames: []string{"tr0", "tr1"}},
				sourceQualification: &sourceQualificationRuntime{
					apply: func(v bool) error { actions = append(actions, v); return nil },
				},
			}
			cm := attachQualificationClock(t, p, utils.QualificationConfig{UseS3: useS3, QualifiedThreshold: 100, DisqualifiedThreshold: 200, QualifiedSamples: 2, DisqualifiedSamples: 1})
			qualified := func() bool {
				result, exists := cm.SourceQualification(p.configName)
				return exists && result.Qualified
			}
			metric := &parser.Metrics{Iface: "master", Offset: 10, ServoState: "s3"}
			p.forwardSourceData(metric, nil)
			require.False(t, qualified(), "no active valid source yet")
			p.forwardSourceData(nil, &parser.PTPEvent{Iface: "tr0", Role: parserconstants.PortRoleSlave})
			p.forwardSourceData(metric, nil)
			p.forwardSourceData(nil, nil) // unrelated line is not another sample
			if !useS3 {
				require.False(t, qualified())
			}
			p.forwardSourceData(metric, nil)
			require.True(t, qualified())
			require.Equal(t, []bool{false, true}, actions)
			p.forwardSourceData(metric, nil)
			require.Len(t, actions, 2, "no repeated pin application on unchanged readiness")
			p.forwardSourceData(nil, &parser.PTPEvent{Iface: "tr0", Role: parserconstants.PortRoleUnknown})
			require.True(t, qualified(), "unknown port diagnostics are not role-loss events")
			metric.ServoState = "s2"
			metric.Offset = 201
			p.forwardSourceData(metric, nil)
			require.False(t, qualified())
			metric.ServoState = "s3"
			metric.Offset = 10
			p.forwardSourceData(metric, nil)
			p.forwardSourceData(metric, nil)
			require.True(t, qualified(), "servo degradation is not port loss; no extra aggregate InSync prerequisite")
			p.forwardSourceData(nil, &parser.PTPEvent{Iface: "tr1", Role: parserconstants.PortRoleSlave})
			require.True(t, qualified(), "a BMCA port switchover must not reset readiness")
			wrongPort := *metric
			wrongPort.Iface = "tr0"
			p.forwardSourceData(&wrongPort, nil)
			require.True(t, qualified(), "samples from a non-selected port are ignored")
			p.forwardSourceData(nil, &parser.PTPEvent{Iface: "tr1", Role: parserconstants.PortRoleMaster})
			require.True(t, qualified(), "a port leaving SLAVE is not a source-loss signal")
		})
	}
}

func TestSourceQualificationHardwareFailure(t *testing.T) {
	t.Parallel()
	fail := true
	p := &ptpProcess{ExecProcess: ExecProcess{name: event.Ptp4lProcessName, configName: "ptp4l.0.config", eventCh: make(chan event.Event, 10)}, nodeProfile: &ptpv1.PtpProfile{},
		tBCAttributes: tBCProcessAttributes{activePort: "tr0", trIfaceNames: []string{"tr0"}},
		sourceQualification: &sourceQualificationRuntime{apply: func(bool) error {
			if fail {
				return errors.New("pin update failed")
			}
			return nil
		}},
	}
	cm := attachQualificationClock(t, p, utils.QualificationConfig{QualifiedThreshold: 100, DisqualifiedThreshold: 200, QualifiedSamples: 1, DisqualifiedSamples: 1})
	p.forwardSourceData(nil, &parser.PTPEvent{Iface: "tr0", Role: parserconstants.PortRoleSlave})
	p.forwardSourceData(&parser.Metrics{Iface: "tr0", Offset: 10, ServoState: "s2"}, nil)
	require.NotEqual(t, event.PTP_LOCKED, p.tBCAttributes.appliedCondition)
	result, exists := cm.SourceQualification(p.configName)
	require.True(t, exists && result.Qualified, "hardware failure must not hide source facts")
	fail = false
	p.forwardSourceData(&parser.Metrics{Iface: "tr0", Offset: 10, ServoState: "s2"}, nil)
	require.Equal(t, event.PTP_LOCKED, p.tBCAttributes.appliedCondition, "next fresh sample retries failed hardware action")
}

func TestSourceQualificationRejectsInvalidProfileWithoutPanic(t *testing.T) {
	t.Parallel()
	p := &ptpv1.PtpProfile{Name: utils.ToPtr("invalid"), Ptp4lOpts: utils.ToPtr("-2 -m"), PtpSettings: map[string]string{"clockType": TBC},
		PtpClockThreshold: &ptpv1.PtpClockThreshold{PtpSourceQualifiedThreshold: utils.ToPtr(int64(0))}}
	dn := &Daemon{}
	var err error
	require.NotPanics(t, func() {
		err = dn.createProcesses(0, p, &ProfileTopology{}, event.TBC, "leading", []string{"tr0"})
	})
	require.Error(t, err)
}

func TestSourceQualificationDoesNotUseUnrelatedHAGroup(t *testing.T) {
	t.Parallel()
	p := &ptpv1.PtpProfile{Name: utils.ToPtr("standalone"), Phc2sysOpts: utils.ToPtr("-a -r"), PtpSettings: map[string]string{"clockType": TBC},
		PtpClockThreshold: &ptpv1.PtpClockThreshold{PtpSourceQualifiedSamples: utils.ToPtr(int64(1))}}
	env := ptpProcessEnv{runID: 3, nodeProfile: p, clockType: event.TBC, upstreamPorts: []string{"eth0"}, topology: &ProfileTopology{
		haProfile: "ha", haMembers: []string{"member0", "member1"}, profileRunID: map[string]int{"member0": 0, "member1": 1},
	}}
	cond := phc2sysOffsetStartCondition(env).(process.StateAwareCondition)
	require.False(t, cond.MetByCurrentState(nil, nil, process.EventStats{SourceQualified: map[string]bool{"ptp4l.0.config": true}}))
	require.True(t, cond.MetByCurrentState(nil, nil, process.EventStats{SourceQualified: map[string]bool{"ptp4l.3.config": true}}))
	ts := ts2phcConditionsForTBC(&ptpProcess{}, env)[process.ActionStart].(process.StateAwareCondition)
	stats := process.EventStats{SourceQualified: map[string]bool{"ptp4l.3.config": true}, States: map[string]map[event.EventSource]event.PTPState{
		"ptp4l.3.config": {event.PHC2SYS: event.PTP_LOCKED},
	}}
	require.True(t, ts.MetByCurrentState(nil, nil, stats), "normal phc2sys still reports under its ptp4l tag even with another HA group present")
}

func TestQualifiedStartupRetainsPhc2sysLock(t *testing.T) {
	t.Parallel()
	profile := &ptpv1.PtpProfile{Name: utils.ToPtr("test"), PtpSettings: map[string]string{"clockType": TBC}, Phc2sysOpts: utils.ToPtr("-a -r"),
		PtpClockThreshold: &ptpv1.PtpClockThreshold{PtpSourceQualifiedSamples: utils.ToPtr(int64(2))}}
	env := ptpProcessEnv{nodeProfile: profile, clockType: event.TBC, upstreamPorts: []string{"eth0"}, topology: &ProfileTopology{}}
	p := &ptpProcess{}
	cond := ts2phcConditionsForTBC(p, env)[process.ActionStart].(process.StateAwareCondition)
	stats := process.EventStats{SourceQualified: map[string]bool{"ptp4l.0.config": true}, States: map[string]map[event.EventSource]event.PTPState{}}
	require.False(t, cond.MetByCurrentState(p, func(string, string) bool { return true }, stats))
	stats.States["ptp4l.0.config"] = map[event.EventSource]event.PTPState{event.PHC2SYS: event.PTP_LOCKED}
	require.True(t, cond.MetByCurrentState(p, nil, stats))
	stats.SourceQualified["ptp4l.0.config"] = false
	require.False(t, cond.MetByCurrentState(p, nil, stats))
	phc := phc2sysOffsetStartCondition(env).(process.StateAwareCondition)
	require.False(t, phc.MetByCurrentState(p, nil, stats))
	stats.SourceQualified["ptp4l.0.config"] = true
	require.True(t, phc.MetByCurrentState(p, nil, stats), "aggregate InSync is not a startup condition")
}

func TestSourceQualificationCompatibilityConditions(t *testing.T) {
	t.Parallel()
	profile := &ptpv1.PtpProfile{Name: utils.ToPtr("test"), PtpSettings: map[string]string{"clockType": TBC}}
	env := ptpProcessEnv{nodeProfile: profile, clockType: event.TBC, upstreamPorts: []string{"eth0"}, topology: &ProfileTopology{}}
	require.IsType(t, process.OnSourceQualified{}, phc2sysOffsetStartCondition(env), "TBC uses source qualification startup by default")
	profile.PtpClockThreshold = &ptpv1.PtpClockThreshold{PtpSourceQualifiedSamples: utils.ToPtr(int64(1))}
	cond := ts2phcConditionsForTBC(&ptpProcess{}, env)[process.ActionStart].(process.StateAwareCondition)
	stats := process.EventStats{SourceQualified: map[string]bool{"ptp4l.0.config": true}}
	require.True(t, cond.MetByCurrentState(nil, nil, stats), "no phc2sys configured means no synthetic lock prerequisite")
	profile.PtpClockThreshold.PtpSourceQualifiedSamples = utils.ToPtr(int64(0))
	require.IsType(t, process.Never{}, phc2sysOffsetStartCondition(env), "invalid policy fails closed")
	require.IsType(t, process.Never{}, ts2phcConditionsForTBC(&ptpProcess{}, env)[process.ActionStart])
}

func TestSourceQualificationRequiresUpstreamPort(t *testing.T) {
	t.Parallel()
	profile := &ptpv1.PtpProfile{Name: utils.ToPtr("test"), PtpSettings: map[string]string{"clockType": TBC}}
	env := ptpProcessEnv{nodeProfile: profile, clockType: event.TBC, topology: &ProfileTopology{}}

	// A T-BC without an upstream port never configures source qualification, so
	// phc2sys and ts2phc must not wait on OnSourceQualified (which would never be
	// produced). phc2sys falls back to the offset-based startup condition.
	require.IsType(t, process.OnStateAndOffsetForCount{}, phc2sysOffsetStartCondition(env))

	ts := ts2phcConditionsForTBC(&ptpProcess{}, env)[process.ActionStart].(*process.All)
	for _, cond := range ts.Conditions {
		_, isQualified := cond.(process.OnSourceQualified)
		require.False(t, isQualified, "ts2phc must not wait on OnSourceQualified without an upstream port")
	}
}

func TestSourceQualificationPropagatesPluginFailure(t *testing.T) {
	t.Parallel()
	profile := &ptpv1.PtpProfile{Name: utils.ToPtr("plugin-failure"), PtpSettings: map[string]string{"clockType": TBC},
		PtpClockThreshold: &ptpv1.PtpClockThreshold{PtpSourceQualifiedSamples: utils.ToPtr(int64(1))}}
	p := &ptpProcess{nodeProfile: profile, tBCAttributes: tBCProcessAttributes{trIfaceNames: []string{"tr0"}},
		dn: &Daemon{processManager: &ProcessManager{clockMgr: clockmgr.Init("test", make(chan event.Event), nil, nil, nil, nil)}},
		pm: &plugin.PluginManager{Plugins: map[string]*plugin.Plugin{
			"failing": {AfterRunPTPCommand: func(_ *interface{}, _ *ptpv1.PtpProfile, _ string) error {
				return errors.New("pin command failed")
			}},
		}},
	}
	clk, err := clock.NewTBC(p.configName, p.dn.processManager.clockMgr.GetUtcOffset, &pmc.MockClient{}, "ptp4.1.config")
	require.NoError(t, err)
	require.NoError(t, p.dn.processManager.clockMgr.AddClock(clk))
	policy, err := resolveSourceQualificationPolicy(profile)
	require.NoError(t, err)
	require.NoError(t, p.configureSourceQualification(policy))
	require.ErrorContains(t, p.sourceQualification.apply(true), "pin command failed")
	require.ErrorContains(t, p.sourceQualification.apply(false), "pin command failed")
}

func TestSourceThresholdPassthrough(t *testing.T) {
	t.Parallel()
	p := &ptpv1.PtpProfile{PtpClockThreshold: &ptpv1.PtpClockThreshold{
		HoldOverTimeout: 5, MaxOffsetThreshold: 100,
		PtpSourceQualifiedThreshold: utils.ToPtr(int64(20)), PtpSourceDisqualifiedThreshold: utils.ToPtr(int64(40)),
		PtpSourceQualifiedSamples: utils.ToPtr(int64(2)), PtpSourceDisqualifiedSamples: utils.ToPtr(int64(3)), PtpSourceUseS3: true,
	}}
	got := getPTPThreshold(p)
	require.Equal(t, p.PtpClockThreshold, got)
	*got.PtpSourceQualifiedSamples = 9
	require.EqualValues(t, 2, *p.PtpClockThreshold.PtpSourceQualifiedSamples, "resolved settings must not mutate API input")
}

func TestSourceQualificationParsedPipeline(t *testing.T) {
	// The real metrics adapter uses shared metric/offset maps; keep this test
	// sequential like the existing parser-to-daemon integration tests.
	InitializeOffsetMaps()
	profile := &ptpv1.PtpProfile{Name: utils.ToPtr("qualified"), PtpSettings: map[string]string{"clockType": TBC},
		PtpClockThreshold: &ptpv1.PtpClockThreshold{MaxOffsetThreshold: 100, PtpSourceQualifiedThreshold: utils.ToPtr(int64(20)), PtpSourceDisqualifiedThreshold: utils.ToPtr(int64(40)), PtpSourceQualifiedSamples: utils.ToPtr(int64(2))}}
	events := make(chan event.Event, 20)
	cm := clockmgr.Init("node", events, nil, nil, nil, nil)
	clk, err := clock.NewTBC("ptp4l.0.config", cm.GetUtcOffset, &pmc.MockClient{}, "ptp4.1.config")
	require.NoError(t, err)
	require.NoError(t, cm.AddClock(clk))
	// Source loss is now parent-dataset-driven; point the parent data set at a
	// remote grandmaster so the offset qualification can proceed.
	clk.AddEvent(event.Event{Source: event.PMC, CfgName: "ptp4l.0.config", Data: &event.ParentDSData{
		ParentDataSet: protocol.ParentDataSet{GrandmasterIdentity: "aabbcc.fffe.ddeeff", GrandmasterClockClass: 6},
	}})
	var hooks []string
	plugins := &plugin.PluginManager{Plugins: map[string]*plugin.Plugin{
		"observer": {AfterRunPTPCommand: func(_ *interface{}, _ *ptpv1.PtpProfile, command string) error {
			hooks = append(hooks, command)
			return nil
		}},
	}}
	p := &ptpProcess{
		ExecProcess: ExecProcess{name: event.Ptp4lProcessName, configName: "ptp4l.0.config", messageTag: "[ptp4l.0.config:{level}]", eventCh: events},
		nodeProfile: profile, clockType: event.TBC, logParser: parser.NewPTP4LExtractor(), ptpClockThreshold: getPTPThreshold(profile),
		ifaces: config.IFaces{{Name: "tr0"}}, pm: plugins,
		dn:            &Daemon{processManager: &ProcessManager{clockMgr: cm}},
		tBCAttributes: tBCProcessAttributes{trPortsConfigFile: "ptp4l.0.config", trIfaceNames: []string{"tr0"}, offsetThreshold: 1},
	}
	policy, err := resolveSourceQualificationPolicy(profile)
	require.NoError(t, err)
	require.NoError(t, p.configureSourceQualification(policy))
	p.sourceQualification.ctx = startQualificationClock(t, cm)
	updates := cm.StatsUpdatesCh()
	qualified := false
	readiness := func() bool {
		// Consume pushed snapshots like ProcessManager. An unrelated log does
		// not publish a sample, so retain the latest evaluated readiness.
		for {
			select {
			case stats := <-updates:
				qualified = stats.SourceQualified[p.configName]
			default:
				return qualified
			}
		}
	}
	p.processOutput("ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED")
	line := "ptp4l[365195.391]: [ptp4l.0.config] master offset -10 s2 freq -3972 path delay 89"
	require.Equal(t, line, p.processOutput(line))
	require.False(t, readiness())
	p.processOutput("unrelated diagnostic")
	require.False(t, readiness(), "unrelated logs cannot count cached offsets")
	p.processOutput(line)
	require.True(t, readiness(), "fresh samples qualify independently of the legacy threshold/filter")
	p.processOutput("another unrelated diagnostic")
	require.True(t, readiness(), "without a new snapshot, retain the last evaluated readiness")
	p.processOutput("ptp4l[4268780.809]: [ptp4l.0.config] port 1: SLAVE to MASTER on ANNOUNCE_RECEIPT_TIMEOUT_EXPIRES")
	require.True(t, readiness(), "a port leaving SLAVE is not a source-loss signal")
	p.invalidateSourceQualification()
	require.False(t, readiness())
	require.Equal(t, []string{"tbc-ho-entry", "tbc-ho-exit", "tbc-ho-entry"}, hooks)
	policy.config.UseS3 = true
	require.ErrorContains(t, p.configureSourceQualification(policy), "requires HardwareConfig")
}

func attachQualificationClock(t *testing.T, p *ptpProcess, policy utils.QualificationConfig) *clockmgr.ClockManager {
	t.Helper()
	cm := clockmgr.Init("test", p.eventCh, nil, nil, nil, nil)
	clk, err := clock.NewTBC(p.configName, cm.GetUtcOffset, &pmc.MockClient{}, "ptp4.1.config")
	require.NoError(t, err)
	require.NoError(t, cm.AddClock(clk))
	generation, err := cm.ConfigureSourceQualification(p.configName, p.tBCAttributes.trIfaceNames, policy)
	require.NoError(t, err)
	p.sourceQualification.generation = generation
	// Source loss is now parent-dataset-driven; point the parent data set at a
	// remote grandmaster so the offset qualification can proceed.
	clk.AddEvent(event.Event{Source: event.PMC, CfgName: p.configName, Data: &event.ParentDSData{
		ParentDataSet: protocol.ParentDataSet{GrandmasterIdentity: "aabbcc.fffe.ddeeff", GrandmasterClockClass: 6},
	}})
	p.sourceQualification.ctx = startQualificationClock(t, cm)
	return cm
}

func startQualificationClock(t *testing.T, cm *clockmgr.ClockManager) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); cm.ProcessEvents(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return ctx
}

func TestSourceQualificationHardwareDoesNotBlockEventPipeline(t *testing.T) {
	in, out := make(chan event.Event, 20), make(chan event.Event, 20)
	cm := clockmgr.Init("test", out, nil, nil, nil, nil)
	clk, err := clock.NewTBC("ptp4l.0.config", cm.GetUtcOffset, &pmc.MockClient{}, "ptp4.1.config")
	require.NoError(t, err)
	require.NoError(t, cm.AddClock(clk))
	// Source loss is now parent-dataset-driven; point the parent data set at a
	// remote grandmaster so the offset qualification can proceed.
	clk.AddEvent(event.Event{Source: event.PMC, CfgName: "ptp4l.0.config", Data: &event.ParentDSData{
		ParentDataSet: protocol.ParentDataSet{GrandmasterIdentity: "aabbcc.fffe.ddeeff", GrandmasterClockClass: 6},
	}})
	generation, err := cm.ConfigureSourceQualification("ptp4l.0.config", []string{"tr0"}, utils.QualificationConfig{
		QualifiedThreshold: 100, DisqualifiedThreshold: 200, QualifiedSamples: 1, DisqualifiedSamples: 1})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	started, release, scanDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	p := &ptpProcess{ExecProcess: ExecProcess{name: event.Ptp4lProcessName, configName: "ptp4l.0.config", eventCh: in}, nodeProfile: &ptpv1.PtpProfile{},
		sourceQualification: &sourceQualificationRuntime{generation: generation, ctx: ctx, apply: func(qualified bool) error {
			if qualified {
				close(started)
				<-release
			}
			return nil
		}}}
	pm := &ProcessManager{eventsIn: in, eventsOut: out, clockMgr: cm, statsIn: cm.StatsUpdatesCh()}
	pmDone, cmDone := make(chan struct{}), make(chan struct{})
	go func() { defer close(pmDone); pm.processEvents(ctx) }()
	go func() { defer close(cmDone); cm.ProcessEvents(ctx) }()
	t.Cleanup(func() { finish(); cancel(); <-scanDone; <-pmDone; <-cmDone })
	go func() {
		defer close(scanDone)
		p.forwardSourceData(nil, &parser.PTPEvent{Iface: "tr0", Role: parserconstants.PortRoleSlave})
		p.forwardSourceData(&parser.Metrics{Iface: "master", Offset: 10, ServoState: "s2"}, nil)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("hardware action did not start")
	}
	// The scanner is held in hardware I/O. Other events and clock snapshots
	// must still flow through both managers, without a second hardware worker.
	reply := make(chan event.SourceQualification, 1)
	in <- event.Event{Source: event.PTP4l, CfgName: p.configName, IFace: "tr0", Data: &event.PTPSourceObservation{
		Generation: generation, ServoState: "s2", Offset: 9, Reply: reply}}
	select {
	case result := <-reply:
		require.True(t, result.Qualified, "both managers must process observations while hardware I/O is blocked")
	case <-time.After(time.Second):
		t.Fatal("hardware action blocked the event pipeline")
	}
	closed := make(chan struct{})
	go func() { p.sourceQualification.close(); close(closed) }()
	finish()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("teardown did not join the hardware action")
	}
}

func TestSourceQualificationShutdownCancelsReplyWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &ptpProcess{ExecProcess: ExecProcess{eventCh: make(chan event.Event, 1)}, sourceQualification: &sourceQualificationRuntime{ctx: ctx}}
	done := make(chan struct{})
	go func() { p.observeSource("", &event.PTPSourceObservation{SourceLost: true}); close(done) }()
	require.Eventually(t, func() bool { return len(p.eventCh) == 1 }, time.Second, time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scanner remained blocked after its consumer stopped")
	}
}

func TestSourceQualificationDropsWhenEventChannelFull(t *testing.T) {
	for _, tc := range []struct {
		name     string
		capacity int
	}{{"unbuffered without receiver", 0}, {"full buffered channel", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			applied := make(chan bool, 1)
			p := &ptpProcess{ExecProcess: ExecProcess{name: event.Ptp4lProcessName, configName: "ptp4l.0.config", eventCh: make(chan event.Event, tc.capacity)},
				sourceQualification: &sourceQualificationRuntime{ctx: ctx, apply: func(value bool) error { applied <- value; return nil }}}
			if tc.capacity > 0 {
				p.eventCh <- event.Event{}
			}
			done := make(chan struct{})
			go func() { p.observeSource("tr0", &event.PTPSourceObservation{ServoState: "s2", Offset: 10}); close(done) }()
			t.Cleanup(func() { cancel(); <-done })
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("full event channel blocked source forwarding")
			}
			require.Len(t, p.eventCh, tc.capacity, "a dropped observation must not replace pending events")
			require.Empty(t, applied, "dropped observations must not apply hardware changes")
		})
	}
}

func TestSourceQualificationTransitionsWaitForDeliveryOrShutdown(t *testing.T) {
	for _, tc := range []struct {
		name        string
		observation event.PTPSourceObservation
	}{
		{"port selected", event.PTPSourceObservation{PortRole: utils.ToPtr(parserconstants.PortRoleSlave)}},
		{"port lost", event.PTPSourceObservation{PortRole: utils.ToPtr(parserconstants.PortRoleMaster)}},
		{"source lost", event.PTPSourceObservation{SourceLost: true}},
	} {
		for _, outcome := range []string{"delivery", "shutdown"} {
			t.Run(tc.name+"/"+outcome, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				applied := make(chan bool, 1)
				p := &ptpProcess{ExecProcess: ExecProcess{name: event.Ptp4lProcessName, configName: "ptp4l.0.config", eventCh: make(chan event.Event, 1)},
					tBCAttributes: tBCProcessAttributes{reportedPtp4lState: event.PTP_FREERUN},
					sourceQualification: &sourceQualificationRuntime{ctx: ctx, generation: 7,
						apply: func(value bool) error { applied <- value; return nil }}}
				p.eventCh <- event.Event{CfgName: "pending"}
				observation := tc.observation
				done := make(chan struct{})
				go func() {
					if observation.SourceLost {
						p.invalidateSourceQualification()
					} else {
						p.observeSource("tr0", &observation)
					}
					close(done)
				}()
				t.Cleanup(func() { cancel(); <-done })
				select {
				case <-done:
					t.Fatal("critical transition was dropped while the event channel was full")
				case <-time.After(20 * time.Millisecond):
				}
				if outcome == "shutdown" {
					cancel()
				} else {
					require.Equal(t, "pending", (<-p.eventCh).CfgName)
					select {
					case ev := <-p.eventCh:
						data := ev.Data.(*event.PTPSourceObservation)
						require.Equal(t, observation.PortRole, data.PortRole)
						require.Equal(t, observation.SourceLost, data.SourceLost)
						require.Equal(t, uint64(7), data.Generation)
						data.Reply <- event.SourceQualification{SourceType: event.PTP4l, IFace: "tr0", Generation: data.Generation}
					case <-time.After(time.Second):
						t.Fatal("critical transition was not delivered after capacity became available")
					}
				}
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("source forwarding did not finish")
				}
				if outcome == "shutdown" {
					require.Len(t, p.eventCh, 1)
					require.Empty(t, applied)
				} else {
					require.Len(t, applied, 1)
					require.False(t, <-applied)
				}
			})
		}
	}
}
