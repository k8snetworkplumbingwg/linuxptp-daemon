package clock

import (
	"context"
	"fmt"
	"testing"
	"time"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/stretchr/testify/require"
)

func pmcRevision(worker *pmcReconciler) uint64 {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return worker.revision
}

func TestGMFixedAccuracyClassesKeepAppliedFallback(t *testing.T) {
	for _, class := range []fbprotocol.ClockClass{fbprotocol.ClockClass6, protocol.ClockClassFreerun} {
		t.Run(fmt.Sprint(class), func(t *testing.T) {
			gm, _, _ := newTestGMClock()
			installPMCHarness(t, gm.pmcWorker)
			gm.pmcRequested = true
			gm.pmcDesired = gmPMCSnapshot{class: fbprotocol.ClockClass7, accuracy: fbprotocol.ClockAccuracyFromOffset(time.Microsecond)}
			gm.announcedClockAccuracy = fbprotocol.ClockAccuracyNanosecond100
			gm.announceClockClassIfChanged(event.Event{Source: event.GNSS}, SyncState{ClockClass: class})
			require.Equal(t, fbprotocol.ClockAccuracyNanosecond100, gm.pmcDesired.accuracy)
		})
	}
}

func TestTBCUnqualifiedObservationCancelsUpstreamWork(t *testing.T) {
	for _, stage := range []string{"fetch", "forwarded", "retry"} {
		t.Run(stage, func(t *testing.T) {
			client := &pmcTestClient{requests: make(chan pmcTestRequest, 10)}
			bc := newPMCTestTBCClock(client)
			bc.controlledPortsConfig = "controlled"
			bc.syncState.State = event.PTP_LOCKED
			qualification, err := utils.NewQualificationState(utils.QualificationConfig{QualifiedThreshold: 100, DisqualifiedThreshold: 200, QualifiedSamples: 1, DisqualifiedSamples: 1})
			require.NoError(t, err)
			qualification.Evaluate(utils.QualificationSample{Source: PTPPortSource{Port: "tr0"}, Offset: 10, ServoState: "s2", SourceAvailable: true})
			bc.ptpSource = &ptpSourceQualification{state: qualification, generation: 1, available: true, lastSelectedPort: "tr0"}
			harness := installPMCHarness(t, bc.pmcWorker)
			bc.updateDownstreamData(bc.cfgName)
			request := nextPMCRequest(t, harness, client)
			require.Equal(t, "tbc", request.kind)
			bc.pmcWorker.mu.Lock()
			identity := bc.pmcWorker.fetch.identity
			bc.pmcWorker.mu.Unlock()
			if stage != "fetch" {
				request.reply <- nil
				bc.AddEvent(harness.next(t))
				external := nextPMCRequest(t, harness, client)
				require.Equal(t, "external", external.kind)
				external.reply <- nil
				request = nextPMCRequest(t, harness, client)
				require.Equal(t, "settings", request.kind)
				if stage == "retry" {
					request.reply <- fmt.Errorf("temporary forwarded settings failure")
					external = nextPMCRequest(t, harness, client)
					require.Equal(t, "external", external.kind)
					external.reply <- nil
					request = nextPMCRequest(t, harness, client)
					require.Equal(t, "settings", request.kind)
				}
			}
			loss := event.Event{Source: event.PTP4l, IFace: "tr0", CfgName: bc.cfgName}
			require.False(t, bc.ObserveSource(loss, &event.PTPSourceObservation{Generation: 1, SourceLost: true}).Qualified)
			require.ErrorIs(t, request.ctx.Err(), context.Canceled)
			require.False(t, bc.pmcWorker.current(identity))
			bc.AddEvent(event.Event{Source: event.PMC, CfgName: bc.cfgName, Data: &identity})
			require.Nil(t, bc.pmcPending)
			require.Nil(t, bc.pmcApplied)
		})
	}
}

func TestGMPendingAccuracySurvivesNonDPLLObservations(t *testing.T) {
	for _, source := range []event.EventSource{event.GNSS, event.TS2PHC} {
		for _, stage := range []string{"fetch", "apply", "retry"} {
			t.Run(string(source)+"/"+stage, func(t *testing.T) {
				client := &pmcTestClient{requests: make(chan pmcTestRequest, 10)}
				client.GMSettingsResult.ClockQuality = fbprotocol.ClockQuality{ClockClass: fbprotocol.ClockClass7, ClockAccuracy: fbprotocol.ClockAccuracyNanosecond100}
				gm, _, _ := newTestGMClock()
				gm.pmcWorker = newPMCReconciler(client, gm.cfgName, event.GM, newPMCTestTime())
				harness := installPMCHarness(t, gm.pmcWorker)
				gm.data = []*event.Data{gmData(event.GNSS, event.PTP_FREERUN), gmData(event.DPLL, event.PTP_HOLDOVER), gmData(event.TS2PHC, event.PTP_FREERUN)}
				gm.announcedClockClass = fbprotocol.ClockClass7
				gm.announcedClockAccuracy = fbprotocol.ClockAccuracyNanosecond100
				offset := int64(1000)
				input := event.Event{Source: event.DPLL, IFace: testTBCIface, CfgName: gm.cfgName, Data: &event.DPLLData{State: event.PTP_HOLDOVER, Offset: &offset}}
				require.Equal(t, fbprotocol.ClockClass7, gm.AddEvent(input).ClockClass)
				accuracy := fbprotocol.ClockAccuracyFromOffset(time.Duration(offset) * time.Nanosecond)
				request := nextPMCRequest(t, harness, client)
				require.Equal(t, "gm", request.kind)
				if stage != "fetch" {
					request.reply <- nil
					gm.AddEvent(harness.next(t))
					request = nextPMCRequest(t, harness, client)
					require.Equal(t, "settings", request.kind)
					if stage == "retry" {
						request.reply <- fmt.Errorf("temporary settings failure")
						request = nextPMCRequest(t, harness, client)
						require.Equal(t, "settings", request.kind)
					}
				}
				revision := pmcRevision(gm.pmcWorker)
				observation := event.Event{Source: source, IFace: testTBCIface, CfgName: gm.cfgName, Data: &event.GNSSData{}}
				if source == event.TS2PHC {
					observation.Data = &event.OffsetData{State: event.PTP_FREERUN}
				}
				for range 5 {
					require.Equal(t, fbprotocol.ClockClass7, gm.AddEvent(observation).ClockClass)
					require.Equal(t, accuracy, gm.pmcDesired.accuracy)
					require.Equal(t, revision, pmcRevision(gm.pmcWorker))
					require.NoError(t, request.ctx.Err())
					require.Equal(t, fbprotocol.ClockAccuracyNanosecond100, gm.announcedClockAccuracy)
				}
				request.reply <- nil
				if stage == "fetch" {
					gm.AddEvent(harness.next(t))
					request = nextPMCRequest(t, harness, client)
					require.Equal(t, "settings", request.kind)
				}
				require.Equal(t, accuracy, request.operation.settings.ClockQuality.ClockAccuracy)
				if stage == "fetch" {
					request.reply <- nil
				}
				gm.AddEvent(harness.next(t))
				require.Equal(t, accuracy, gm.announcedClockAccuracy)
				gm.AddEvent(observation)
				require.Equal(t, revision, pmcRevision(gm.pmcWorker))
				require.Equal(t, int64(1000), offset)
			})
		}
	}
}

func TestTBCLocalDowngradeSurvivesUnqualifiedObservations(t *testing.T) {
	for _, state := range []event.PTPState{event.PTP_HOLDOVER, event.PTP_FREERUN} {
		for _, stage := range []string{"blocked", "retry"} {
			t.Run(string(state)+"/"+stage, func(t *testing.T) {
				client := &pmcTestClient{requests: make(chan pmcTestRequest, 10)}
				client.ParentTimeCurrentDSResult.ParentDataSet = protocol.ParentDataSet{GrandmasterClockClass: 6, GrandmasterIdentity: "aabb.ccdd.eeff"}
				bc := newPMCTestTBCClock(client)
				bc.controlledPortsConfig = "controlled"
				bc.leadingClockData.leadingInterface = "tr0"
				bc.leadingClockData.toFreeRunThreshold = 10000
				bc.leadingClockData.MaxInSpecOffset = 10000
				bc.leadingClockData.inSyncConditionThreshold = 100
				bc.leadingClockData.initInSyncFilter()
				bc.syncState.State = event.PTP_LOCKED
				bc.syncState.ClockClass = fbprotocol.ClockClass6
				bc.data = []*event.Data{gmData(event.DPLL, state)}
				bc.data[0].Details[0].IFace = "tr0"
				if state == event.PTP_FREERUN {
					bc.data[0].Details[0].Offset = 20000
				}
				qualification, err := utils.NewQualificationState(utils.QualificationConfig{QualifiedThreshold: 100, DisqualifiedThreshold: 200, QualifiedSamples: 1, DisqualifiedSamples: 1})
				require.NoError(t, err)
				qualification.Evaluate(utils.QualificationSample{Source: PTPPortSource{Port: "tr0"}, Offset: 10, ServoState: "s2", SourceAvailable: true})
				bc.ptpSource = &ptpSourceQualification{state: qualification, generation: 1, available: true, lastSelectedPort: "tr0", configuredUpstreamPorts: []string{"tr0"}}
				harness := installPMCHarness(t, bc.pmcWorker)
				bc.updateDownstreamData(bc.cfgName)
				fetch := nextPMCRequest(t, harness, client)
				require.Equal(t, "tbc", fetch.kind)
				fetch.reply <- nil
				stale := harness.next(t)
				loss := event.Event{Source: event.PTP4l, IFace: "tr0", CfgName: bc.cfgName, Data: &event.OffsetData{State: event.PTP_FREERUN, SourceLost: true}}
				require.Equal(t, state, bc.AddEvent(loss).State)
				localClass := bc.syncState.ClockClass
				external := nextPMCRequest(t, harness, client)
				require.Equal(t, "external", external.kind)
				external.reply <- nil
				request := nextPMCRequest(t, harness, client)
				require.Equal(t, "settings", request.kind)
				if stage == "retry" {
					request.reply <- fmt.Errorf("temporary settings failure")
					external = nextPMCRequest(t, harness, client)
					require.Equal(t, "external", external.kind)
					external.reply <- nil
					request = nextPMCRequest(t, harness, client)
					require.Equal(t, "settings", request.kind)
				}
				revision := pmcRevision(bc.pmcWorker)
				for range 5 {
					result := bc.ObserveSource(loss, &event.PTPSourceObservation{Generation: 1, SourceLost: true})
					require.False(t, result.Qualified)
					require.Equal(t, state, bc.AddEvent(loss).State)
					require.Equal(t, localClass, bc.syncState.ClockClass)
					require.Equal(t, revision, pmcRevision(bc.pmcWorker))
					require.NoError(t, request.ctx.Err())
				}
				bc.AddEvent(stale)
				require.Zero(t, bc.leadingClockData.upstreamParentDataSet.GrandmasterClockClass)
				require.Nil(t, bc.pmcPending)
				require.Equal(t, "controlled", request.operation.config)
				require.Equal(t, localClass, request.operation.settings.ClockQuality.ClockClass)
				request.reply <- nil
				local := nextPMCRequest(t, harness, client)
				require.Equal(t, "settings", local.kind)
				require.Equal(t, bc.cfgName, local.operation.config)
				require.Equal(t, request.operation.settings, local.operation.settings)
				local.reply <- nil
				bc.AddEvent(harness.next(t))
				require.Equal(t, revision, pmcRevision(bc.pmcWorker))
				require.Empty(t, client.requests)
				role := constants.PortRoleSlave
				bc.ObserveSource(loss, &event.PTPSourceObservation{Generation: 1, PortRole: &role})
				require.True(t, bc.ObserveSource(loss, &event.PTPSourceObservation{Generation: 1, ServoState: "s2", Offset: 10}).Qualified)
				offset := int64(10)
				bc.AddEvent(event.Event{Source: event.DPLL, CfgName: bc.cfgName, IFace: "tr0", Time: time.Now().UnixMilli(), Data: &event.DPLLData{State: event.PTP_LOCKED, Offset: &offset}})
				for range event.WindowSize + 1 {
					bc.AddEvent(event.Event{Source: event.PTP4l, CfgName: bc.cfgName, IFace: "tr0", Time: time.Now().UnixMilli(), Data: &event.OffsetData{State: event.PTP_LOCKED, Offset: 10}})
					if bc.syncState.State == event.PTP_LOCKED {
						break
					}
				}
				require.Equal(t, event.PTP_LOCKED, bc.syncState.State)
				recovery := nextPMCRequest(t, harness, client)
				require.Equal(t, "tbc", recovery.kind)
				recovery.reply <- nil
				bc.AddEvent(harness.next(t))
				forwardExternal := nextPMCRequest(t, harness, client)
				require.Equal(t, "external", forwardExternal.kind)
				forwardExternal.reply <- nil
				forwardSettings := nextPMCRequest(t, harness, client)
				require.Equal(t, "settings", forwardSettings.kind)
				require.Equal(t, "controlled", forwardSettings.operation.config)
				require.Equal(t, fbprotocol.ClockClass6, forwardSettings.operation.settings.ClockQuality.ClockClass)
				forwardSettings.reply <- nil
				bc.AddEvent(harness.next(t))
				require.Equal(t, client.ParentTimeCurrentDSResult, *bc.pmcApplied)
			})
		}
	}
}
