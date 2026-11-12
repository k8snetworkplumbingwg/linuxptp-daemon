package clock

import (
	"context"
	"fmt"
	"testing"
	"time"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/stretchr/testify/require"
)

type pmcTestRequest struct {
	ctx       context.Context
	operation pmcOperation
	kind      string
	reply     chan error
}

type pmcTestClient struct {
	pmc.MockClient
	requests chan pmcTestRequest
}

type deliveredPMCClient struct {
	pmc.MockClient
	effects chan protocol.GrandmasterSettings
	release chan struct{}
	first   bool
}

func (client *deliveredPMCClient) SetGMSettingsContext(_ context.Context, _ string, settings protocol.GrandmasterSettings) error {
	client.effects <- settings
	if !client.first {
		client.first = true
		<-client.release
	}
	return nil
}

func TestPMCAlreadyDeliveredSETIsRepairedByNewestPlan(t *testing.T) {
	client := &deliveredPMCClient{effects: make(chan protocol.GrandmasterSettings, 2), release: make(chan struct{})}
	worker := newPMCReconciler(client, "local", event.GM, newPMCTestTime())
	harness := installPMCHarness(t, worker)
	old := protocol.GrandmasterSettings{ClockQuality: fbprotocol.ClockQuality{ClockClass: 7}}
	latest := protocol.GrandmasterSettings{ClockQuality: fbprotocol.ClockQuality{ClockClass: 248}}
	require.NoError(t, worker.submit([]pmcOperation{{config: "local", settings: old}}, nil))
	select {
	case effect := <-client.effects:
		require.Equal(t, old, effect)
	case <-time.After(5 * time.Second):
		t.Fatal("first SET missing")
	}
	require.NoError(t, worker.submit([]pmcOperation{{config: "local", settings: latest}}, nil))
	close(client.release)
	result := harness.next(t).Data.(*event.PMCResult)
	require.True(t, worker.current(*result))
	require.Equal(t, latest, <-client.effects)
	require.Empty(t, harness.events, "old canceled acknowledgment cannot certify application")
}

func TestTBCCompletePlansAndSameTargetDeduplication(t *testing.T) {
	properties := protocol.TimePropertiesDS{CurrentUtcOffset: 38, CurrentUtcOffsetValid: true, Leap59: true, Leap61: true, FrequencyTraceable: true, TimeTraceable: true, PtpTimescale: false, TimeSource: fbprotocol.TimeSourceGNSS}
	for _, class := range []fbprotocol.ClockClass{135, 165, 248} {
		t.Run(fmt.Sprint(class), func(t *testing.T) {
			operations := tbcLocalPlan("local", "controlled", "aabb.ccdd.eeff", class, &properties, 37)
			require.Len(t, operations, 3)
			require.Equal(t, pmcOperation{config: "controlled", external: true, properties: protocol.ExternalGrandmasterProperties{GrandmasterIdentity: "aabb.ccdd.eeff"}}, operations[0])
			expected := protocol.GrandmasterSettings{ClockQuality: fbprotocol.ClockQuality{ClockClass: class, ClockAccuracy: fbprotocol.ClockAccuracyUnknown, OffsetScaledLogVariance: 0xffff}, TimePropertiesDS: protocol.TimePropertiesDS{CurrentUtcOffset: 38, CurrentUtcOffsetValid: true, Leap59: true, Leap61: true, PtpTimescale: true, TimeTraceable: class == 135, TimeSource: fbprotocol.TimeSourceInternalOscillator}}
			if class == 248 {
				expected.TimePropertiesDS = protocol.TimePropertiesDS{CurrentUtcOffset: 37, PtpTimescale: true, TimeSource: fbprotocol.TimeSourceInternalOscillator}
			}
			require.Equal(t, pmcOperation{config: "controlled", settings: expected}, operations[1])
			require.Equal(t, pmcOperation{config: "local", settings: expected}, operations[2])
			if class != 248 {
				require.Len(t, tbcLocalPlan("local", "controlled", "aabb.ccdd.eeff", class, nil, 37), 1)
			}
		})
	}
	require.Empty(t, tbcLocalPlan("local", "", "id", 248, nil, 37), "TTSC has no controlled downstream plan")
	datasets := pmc.ParentTimeCurrentDS{ParentDataSet: protocol.ParentDataSet{GrandmasterIdentity: "aabb.ccdd.eeff", GrandmasterClockClass: 6, GrandmasterClockAccuracy: 0x21, GrandmasterOffsetScaledLogVariance: 0x4e5d}, TimePropertiesDS: properties, CurrentDS: protocol.CurrentDS{StepsRemoved: 9}}
	forward := tbcForwardPlan("controlled", datasets)
	require.Equal(t, []pmcOperation{{config: "controlled", external: true, properties: protocol.ExternalGrandmasterProperties{GrandmasterIdentity: "aabb.ccdd.eeff", StepsRemoved: 9}}, {config: "controlled", settings: protocol.GrandmasterSettings{ClockQuality: fbprotocol.ClockQuality{ClockClass: 6, ClockAccuracy: 0x21, OffsetScaledLogVariance: 0x4e5d}, TimePropertiesDS: properties}}}, forward)
	client := &pmc.MockClient{}
	worker := newPMCReconciler(client, "controlled", event.TBC, newPMCTestTime())
	harness := installPMCHarness(t, worker)
	require.NoError(t, worker.submit(tbcLocalPlan("controlled", "controlled", "aabb.ccdd.eeff", 248, nil, 37), nil))
	require.Equal(t, "applied", harness.next(t).Data.(*event.PMCResult).Kind)
	require.Len(t, client.SnapshotSetCalls(), 2)
}

func TestTBCQueuedFetchAdmissionAndApplicationAreDistinct(t *testing.T) {
	for _, rejection := range []string{"none", "reset", "token", "config", "lock", "owner", "revision", "source-port", "source-generation", "qualification"} {
		t.Run(rejection, func(t *testing.T) {
			client := &pmc.MockClient{ParentTimeCurrentDSResult: pmc.ParentTimeCurrentDS{ParentDataSet: protocol.ParentDataSet{GrandmasterClockClass: 6, GrandmasterIdentity: "aabb.ccdd.eeff"}, CurrentDS: protocol.CurrentDS{StepsRemoved: 3}}}
			bc, err := NewTBC("local", func() int { return 37 }, client, "controlled")
			require.NoError(t, err)
			bc.SetOsClock(&OsClock{})
			harness := installPMCHarness(t, bc.pmcWorker)
			bc.syncState.State = event.PTP_LOCKED
			state, err := utils.NewQualificationState(utils.QualificationConfig{QualifiedThreshold: 100, DisqualifiedThreshold: 200, QualifiedSamples: 1, DisqualifiedSamples: 1})
			require.NoError(t, err)
			state.Evaluate(utils.QualificationSample{Source: PTPPortSource{Port: "tr0"}, Offset: 10, ServoState: "s2", SourceAvailable: true})
			bc.ptpSource = &ptpSourceQualification{state: state, generation: 1, available: true, lastSelectedPort: "tr0"}
			bc.updateDownstreamData("local")
			ev := harness.next(t)
			result := ev.Data.(*event.PMCResult)
			switch rejection {
			case "reset":
				bc.Reset()
			case "token":
				bc.announceToken++
			case "config":
				result.Config = "other"
			case "lock":
				bc.syncState.State = event.PTP_FREERUN
			case "owner":
				result.Owner++
			case "revision":
				result.Revision++
			case "source-port":
				bc.ptpSource.lastSelectedPort = "tr1"
			case "source-generation":
				bc.ptpSource.generation++
			case "qualification":
				bc.ptpSource.state.Reset()
			}
			bc.AddEvent(ev)
			if rejection == "none" {
				require.Equal(t, uint8(6), bc.leadingClockData.downstreamParentDataSet.GrandmasterClockClass)
				require.Nil(t, bc.pmcApplied)
				harness.flush(t, bc.AddEvent)
				require.Equal(t, client.ParentTimeCurrentDSResult, *bc.pmcApplied)
			} else {
				require.Empty(t, client.SnapshotSetCalls())
				require.Nil(t, bc.pmcPending)
				require.Nil(t, bc.pmcApplied)
			}
		})
	}
}

type legacyPMCOnly struct{ pmc.Client }

func TestManagedPMCSetupRequiresCancellableCapabilitiesAndSink(t *testing.T) {
	_, err := NewGM("local", func() int { return 37 }, legacyPMCOnly{})
	require.ErrorContains(t, err, "GMSettingsClient")
	_, err = NewTBC("local", func() int { return 37 }, legacyPMCOnly{}, "controlled")
	require.ErrorContains(t, err, "DownstreamClient")
	client := &pmc.MockClient{}
	worker := newPMCReconciler(client, "local", event.GM, newPMCTestTime())
	require.ErrorContains(t, worker.requestFetch("gm", "local", 0), "sink")
	require.ErrorContains(t, worker.submit(nil, nil), "sink")
	require.Empty(t, client.SnapshotGetCalls())
	<-worker.stop()
}

func TestGMSameClassUTCAndInheritedLeapRefreshAndQueuedReset(t *testing.T) {
	gm, _, client := newTestGMClock()
	harness := installPMCHarness(t, gm.pmcWorker)
	gm.syncState.ClockClass = fbprotocol.ClockClass6
	ev := event.Event{Source: event.GNSS, Data: &event.GNSSData{}}
	state := SyncState{State: event.PTP_LOCKED, ClockClass: fbprotocol.ClockClass6}
	gm.announceClockClassIfChanged(ev, state)
	harness.flush(t, gm.AddEvent)
	first := *client.SnapshotSetCalls()[0].GMSettings
	client.GMSettingsResult = first
	client.GMSettingsResult.TimePropertiesDS.Leap61 = true
	gm.getUtcOffset = func() int { return 38 }
	gm.announceClockClassIfChanged(ev, state)
	harness.flush(t, gm.AddEvent)
	calls := client.SnapshotSetCalls()
	require.Len(t, calls, 2)
	expected := first
	expected.TimePropertiesDS.CurrentUtcOffset = 38
	expected.TimePropertiesDS.Leap61 = true
	require.Equal(t, expected, *calls[1].GMSettings)
	gets := len(client.SnapshotGetCalls())
	gm.pmcWorker.mu.Lock()
	revision := gm.pmcWorker.revision
	gm.pmcWorker.mu.Unlock()
	gm.announceClockClassIfChanged(ev, state)
	require.Len(t, client.SnapshotGetCalls(), gets, "unchanged input must not create a refetch storm")
	gm.pmcWorker.mu.Lock()
	afterRevision := gm.pmcWorker.revision
	gm.pmcWorker.mu.Unlock()
	require.Equal(t, revision, afterRevision, "unchanged input cannot even queue another fetch")
	client.GMSettingsResult = expected
	client.GMSettingsResult.TimePropertiesDS.Leap61 = false
	harness.timer.step(30 * time.Second)
	gm.announceClockClassIfChanged(ev, state)
	gm.AddEvent(harness.next(t))
	applied := harness.next(t)
	require.False(t, applied.Data.(*event.PMCResult).Settings.TimePropertiesDS.Leap61)
	gm.AddEvent(applied)
	require.Len(t, client.SnapshotGetCalls(), gets+1)
	require.Len(t, client.SnapshotSetCalls(), 2, "equal complete baseline needs no redundant SET")
	harness.timer.step(30 * time.Second)
	gm.announceClockClassIfChanged(ev, state)
	queued := harness.next(t)
	gm.Reset()
	gm.AddEvent(queued)
	require.Zero(t, gm.announcedClockClass)
	require.Len(t, client.SnapshotSetCalls(), 2, "queued pre-reset baseline cannot start a SET")
}

func (client *pmcTestClient) call(ctx context.Context, kind string, operation pmcOperation) error {
	request := pmcTestRequest{ctx: ctx, kind: kind, operation: operation, reply: make(chan error, 1)}
	select {
	case client.requests <- request:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-request.reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (client *pmcTestClient) GetGMSettingsContext(ctx context.Context, config string) (protocol.GrandmasterSettings, error) {
	return client.GMSettingsResult, client.call(ctx, "gm", pmcOperation{config: config})
}
func (client *pmcTestClient) GetParentTimeAndCurrentDSContext(ctx context.Context, config string) (pmc.ParentTimeCurrentDS, error) {
	return client.ParentTimeCurrentDSResult, client.call(ctx, "tbc", pmcOperation{config: config})
}
func (client *pmcTestClient) SetGMSettingsContext(ctx context.Context, config string, settings protocol.GrandmasterSettings) error {
	return client.call(ctx, "settings", pmcOperation{config: config, settings: settings})
}
func (client *pmcTestClient) SetExternalGMPropertiesNPContext(ctx context.Context, config string, properties protocol.ExternalGrandmasterProperties) error {
	return client.call(ctx, "external", pmcOperation{config: config, external: true, properties: properties})
}

func nextPMCRequest(t *testing.T, harness *pmcHarness, client *pmcTestClient) pmcTestRequest {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case request := <-client.requests:
			return request
		case timer := <-harness.timer.created:
			harness.timer.mu.Lock()
			delay := timer.when.Sub(harness.timer.now)
			harness.timer.mu.Unlock()
			harness.timer.step(max(0, delay))
		case <-deadline.C:
			t.Fatal("PMC request missing")
			return pmcTestRequest{}
		}
	}
}

func TestPMCOrderedPlansRepairPartialFailures(t *testing.T) {
	operations := []pmcOperation{
		{config: "controlled", external: true, properties: protocol.ExternalGrandmasterProperties{GrandmasterIdentity: "aabb.ccdd.eeff", StepsRemoved: 2}},
		{config: "controlled", settings: protocol.GrandmasterSettings{ClockQuality: fbprotocol.ClockQuality{ClockClass: 135}}},
		{config: "local", settings: protocol.GrandmasterSettings{ClockQuality: fbprotocol.ClockQuality{ClockClass: 135}}},
	}
	for failed := range operations {
		t.Run(operations[failed].config+string(rune('0'+failed)), func(t *testing.T) {
			client := &pmcTestClient{requests: make(chan pmcTestRequest)}
			worker := newPMCReconciler(client, "local", event.TBC, newPMCTestTime())
			harness := installPMCHarness(t, worker)
			require.NoError(t, worker.submit(append(append([]pmcOperation{}, operations...), operations[1]), nil))
			for index := 0; index <= failed; index++ {
				request := nextPMCRequest(t, harness, client)
				require.Equal(t, operations[index], request.operation)
				if index == failed {
					request.reply <- assertPMCFailure{}
				} else {
					request.reply <- nil
				}
			}
			require.Empty(t, harness.events, "partial application is not success")
			for _, operation := range operations {
				request := nextPMCRequest(t, harness, client)
				require.Equal(t, operation, request.operation)
				request.reply <- nil
			}
			require.Equal(t, "applied", harness.next(t).Data.(*event.PMCResult).Kind)
		})
	}
}

type assertPMCFailure struct{}

func (assertPMCFailure) Error() string { return "injected PMC transport failure" }

func TestPMCBurstCancellationResetAndTerminalStop(t *testing.T) {
	client := &pmcTestClient{requests: make(chan pmcTestRequest)}
	worker := newPMCReconciler(client, "local", event.GM, newPMCTestTime())
	harness := installPMCHarness(t, worker)
	require.NoError(t, worker.requestFetch("gm", "local", 0))
	blocked := nextPMCRequest(t, harness, client)
	for revision := 0; revision < 500; revision++ {
		require.NoError(t, worker.submit([]pmcOperation{{config: "local", settings: protocol.GrandmasterSettings{ClockQuality: fbprotocol.ClockQuality{ClockClass: fbprotocol.ClockClass(revision % 200)}}}}, nil))
	}
	<-blocked.ctx.Done()
	request := nextPMCRequest(t, harness, client)
	require.Equal(t, fbprotocol.ClockClass(499%200), request.operation.settings.ClockQuality.ClockClass)
	request.reply <- nil
	result := harness.next(t).Data.(*event.PMCResult)
	worker.reset()
	require.False(t, worker.current(*result))
	require.NoError(t, worker.requestFetch("gm", "local", 0))
	request = nextPMCRequest(t, harness, client)
	request.reply <- nil
	require.Equal(t, "gm", harness.next(t).Data.(*event.PMCResult).Kind)
	done := worker.stop()
	require.Equal(t, done, worker.stop())
	<-done
	require.ErrorContains(t, worker.requestFetch("gm", "local", 0), "stopped")
	require.ErrorContains(t, worker.submit(nil, nil), "stopped")
	never := newPMCReconciler(client, "new", event.GM, newPMCTestTime())
	<-never.stop()
	require.Equal(t, never.stop(), (<-chan struct{})(never.done))
}

func TestPMCFetchBackoffAndNewRevisionRateLimit(t *testing.T) {
	client := &pmcTestClient{requests: make(chan pmcTestRequest)}
	worker := newPMCReconciler(client, "local", event.GM, newPMCTestTime())
	harness := installPMCHarness(t, worker)
	require.NoError(t, worker.requestFetch("gm", "local", 0))
	for _, delay := range []time.Duration{1, 2, 4, 8, 16, 30, 30} {
		request := nextPMCRequest(t, harness, client)
		request.reply <- assertPMCFailure{}
		select {
		case timer := <-harness.timer.created:
			require.Equal(t, delay*time.Second, timer.when.Sub(harness.timer.Now()))
			harness.timer.step(delay * time.Second)
		case <-time.After(5 * time.Second):
			t.Fatal("missing backoff timer")
		}
	}
	blocked := nextPMCRequest(t, harness, client)
	require.NoError(t, worker.requestFetch("gm", "new", 0))
	<-blocked.ctx.Done()
	request := nextPMCRequest(t, harness, client)
	require.Equal(t, "new", request.operation.config)
	request.reply <- nil
	require.Equal(t, "new", harness.next(t).Data.(*event.PMCResult).Config)
}

func TestPMCBlockedResultDeliveryCancelsBeforeJoin(t *testing.T) {
	worker := newPMCReconciler(&pmc.MockClient{}, "local", event.GM, newPMCTestTime())
	entered := make(chan struct{})
	worker.setSink(func(ctx context.Context, _ event.Event) error { close(entered); <-ctx.Done(); return ctx.Err() })
	require.NoError(t, worker.requestFetch("gm", "local", 0))
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("delivery not started")
	}
	select {
	case <-worker.stop():
	case <-time.After(5 * time.Second):
		t.Fatal("delivery cancellation did not join")
	}
}

func TestGMCompletePolicyPreservesInheritedLeapFlags(t *testing.T) {
	for _, class := range []fbprotocol.ClockClass{6, 7, 140, 248} {
		t.Run(fmt.Sprint(class), func(t *testing.T) {
			baseline := protocol.GrandmasterSettings{TimePropertiesDS: protocol.TimePropertiesDS{Leap59: true, Leap61: true}}
			settings, err := gmSettings(baseline, gmPMCSnapshot{class: class, accuracy: fbprotocol.ClockAccuracyMicrosecond1, utc: 38})
			require.NoError(t, err)
			expected := protocol.GrandmasterSettings{ClockQuality: fbprotocol.ClockQuality{ClockClass: class, ClockAccuracy: fbprotocol.ClockAccuracyMicrosecond1, OffsetScaledLogVariance: 0xffff}, TimePropertiesDS: protocol.TimePropertiesDS{Leap59: true, Leap61: true, CurrentUtcOffset: 38, CurrentUtcOffsetValid: true, PtpTimescale: true, FrequencyTraceable: true, TimeTraceable: class == 6 || class == 7, TimeSource: fbprotocol.TimeSourceInternalOscillator}}
			if class == 6 {
				expected.ClockQuality.ClockAccuracy = fbprotocol.ClockAccuracyNanosecond100
				expected.ClockQuality.OffsetScaledLogVariance = 0x4e5d
				expected.TimePropertiesDS.TimeSource = fbprotocol.TimeSourceGNSS
			}
			if class == 248 {
				expected.ClockQuality.ClockAccuracy = fbprotocol.ClockAccuracyUnknown
			}
			require.Equal(t, expected, settings)
		})
	}
}
