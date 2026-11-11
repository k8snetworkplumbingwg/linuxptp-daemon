package clock

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type ts2phcTestClock interface {
	Clock
	observeTS2PHC(event.Event, *event.TS2PHCObservation) ts2phcUpdate
	applyTS2PHC(ts2phcUpdate) SyncState
	ConfigureTS2PHC(event.TS2PHCConfig)
}

func ts2phcTestConfig() *event.TS2PHCConfig {
	return &event.TS2PHCConfig{
		LeadingInterface: "ens0",
		Interfaces: []event.TS2PHCInterface{
			{Name: "ens0", PHCDevice: "/dev/ptp0", Source: event.PPS},
			{Name: "ens1", PHCDevice: "/dev/ptp1", Source: event.PPS},
			{Name: "ens2", PHCDevice: "/dev/ptp2", Source: event.PPS},
		},
		Threshold: event.PtpClockThreshold{MaxOffsetThreshold: 10000},
	}
}

func newTS2PHCTestClock(clockType event.ClockType) (ts2phcTestClock, *ts2phcTracker) {
	var c ts2phcTestClock
	var tracker *ts2phcTracker
	if clockType == event.GM {
		gm, _, _ := newTestGMClock()
		c, tracker = gm, &gm.ts2phc
	} else {
		tbc := newPMCTestTBCClock(nil)
		tbc.SetConfiguredLeadingInterface("ens0")
		c, tracker = tbc, &tbc.ts2phc
	}
	c.ConfigureTS2PHC(*ts2phcTestConfig())
	return c, tracker
}

func ts2phcTestEvent(c ts2phcTestClock, iface string, observation *event.TS2PHCObservation) event.Event {
	return event.Event{
		Source: event.TS2PHC, CfgName: "ts2phc.1.config", IFace: iface,
		ClockType: c.ClockType(), Time: time.Now().UnixMilli(), Data: observation,
	}
}

func observeTS2PHCTest(c ts2phcTestClock, iface string, observation *event.TS2PHCObservation) []event.Event {
	result := c.AddEvent(ts2phcTestEvent(c, iface, observation))
	events := make([]event.Event, 0, len(result.Events))
	for _, report := range result.Events {
		events = append(events, report.Event)
	}
	return events
}

func ts2phcTestStamp(device, logTime string, seconds int64) *event.TS2PHCObservation {
	return &event.TS2PHCObservation{Timestamp: logTime, PHCDevice: device, PHCTstamp: &event.PHCTimestamp{Seconds: seconds}}
}

func ts2phcTestOffset(offset float64) *event.TS2PHCObservation {
	return &event.TS2PHCObservation{PollSuccessful: true, Offset: &offset, State: event.PTP_LOCKED}
}

func TestTS2PHCObservationFiltersBeforeApplying(t *testing.T) {
	for _, clockType := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(clockType), func(t *testing.T) {
			c, _ := newTS2PHCTestClock(clockType)
			recorder := &ipcRecorder{}
			c.SetIPC(recorder.send)
			data := c.GetData(event.TS2PHC)
			data.Details = event.DDetails{{IFace: "ens1", State: event.PTP_FREERUN}}
			data.UpdateState()
			before := c.GetState()

			raw := ts2phcTestOffset(0)
			raw.NMEALocked = utils.ToPtr(true)
			update := c.observeTS2PHC(ts2phcTestEvent(c, "ens1", raw), raw)
			require.Len(t, update.events, 1)
			require.IsType(t, &event.OffsetData{}, update.events[0].Data)
			offset := update.events[0].Data.(*event.OffsetData)
			require.NotNil(t, offset.NMEALocked)
			assert.True(t, *offset.NMEALocked)
			assert.Equal(t, event.PTP_FREERUN, data.GetDataDetails("ens1").State)
			assert.Equal(t, before, c.GetState())
			assert.Empty(t, recorder.messages, "filtering must not publish a clock-state change")

			state := c.applyTS2PHC(update)
			assert.Equal(t, c.GetState(), state.State)
			assert.Equal(t, event.PTP_LOCKED, data.GetDataDetails("ens1").State)
			assert.True(t, *raw.NMEALocked, "the raw observation remains unchanged")
			if clockType == event.GM {
				assert.False(t, *offset.NMEALocked, "GM reporting uses the final clock state")
			}
		})
	}
}

func TestTS2PHCObservationDefersInactiveCleanup(t *testing.T) {
	for _, clockType := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(clockType), func(t *testing.T) {
			c, _ := newTS2PHCTestClock(clockType)
			recorder := &ipcRecorder{}
			c.SetIPC(recorder.send)
			data := c.GetData(event.TS2PHC)
			data.Details = event.DDetails{
				{IFace: "ens1", State: event.PTP_FREERUN},
				{IFace: "ens2", State: event.PTP_LOCKED, Offset: 17, HasOffset: true},
			}
			data.UpdateState()
			before := c.GetState()

			raw := &event.TS2PHCObservation{PHCDevice: "/dev/ptp1", Role: event.TS2PHCUnmanaged}
			update := c.observeTS2PHC(ts2phcTestEvent(c, "", raw), raw)
			assert.Empty(t, update.events)
			assert.Contains(t, update.inactivePHCs, "/dev/ptp1")
			assert.NotNil(t, data.GetDataDetails("ens1"))
			assert.Equal(t, event.PTP_FREERUN, data.State)
			assert.Equal(t, before, c.GetState())
			assert.Empty(t, recorder.messages)

			state := c.applyTS2PHC(update)
			assert.Equal(t, c.GetState(), state.State)
			assert.Nil(t, data.GetDataDetails("ens1"))
			assert.Equal(t, event.PTP_LOCKED, data.State)
			remaining := data.GetDataDetails("ens2")
			require.NotNil(t, remaining)
			assert.Equal(t, int64(17), remaining.Offset)
		})
	}
}

func TestTS2PHCAddEventAcceptsEvaluatedEvents(t *testing.T) {
	for _, clockType := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(clockType), func(t *testing.T) {
			c, _ := newTS2PHCTestClock(clockType)
			state := c.AddEvent(event.Event{
				Source: event.TS2PHC, CfgName: "ts2phc.1.config", IFace: "ens1",
				ClockType: clockType, Time: time.Now().UnixMilli(),
				Data: &event.OffsetData{State: event.PTP_LOCKED, Offset: 42},
			})
			detail := c.GetData(event.TS2PHC).GetDataDetails("ens1")
			require.NotNil(t, detail)
			assert.Equal(t, event.PTP_LOCKED, detail.State)
			assert.Equal(t, c.GetState(), state.State)
		})
	}
}

func TestTS2PHCAddEventProcessesRawObservations(t *testing.T) {
	for _, clockType := range []event.ClockType{event.GM, event.TBC} {
		for _, tc := range []struct {
			name        string
			observation *event.TS2PHCObservation
		}{
			{"observation", ts2phcTestOffset(42)},
			{"nil observation", nil},
		} {
			t.Run(string(clockType)+"/"+tc.name, func(t *testing.T) {
				c, tracker := newTS2PHCTestClock(clockType)
				data := c.GetData(event.TS2PHC)
				data.Details = event.DDetails{{IFace: "ens1", State: event.PTP_FREERUN, Offset: 99, HasOffset: true}}
				data.UpdateState()
				cycle := &pollCycle{group: 2000, tstamps: []tstamp{{device: "/dev/ptp1", role: event.TS2PHCSink}}}
				tracker.currentPollCycle = cycle
				before := c.GetState()

				result := c.AddEvent(ts2phcTestEvent(c, "ens1", tc.observation))
				assert.False(t, result.Ignored)
				assert.Same(t, cycle, tracker.currentPollCycle)
				if tc.observation == nil {
					assert.Empty(t, result.Events)
					assert.Equal(t, before, result.State)
					assert.Equal(t, event.PTP_FREERUN, data.GetDataDetails("ens1").State)
					assert.Equal(t, int64(99), data.GetDataDetails("ens1").Offset)
					assert.False(t, cycle.tstamps[0].handled)
					return
				}
				require.Len(t, result.Events, 1)
				assert.Equal(t, "master", result.Events[0].OffsetSource)
				assert.False(t, result.Events[0].OffsetUnavailable)
				require.Len(t, result.Metrics, 1)
				assert.Equal(t, event.TS2PHC, result.Metrics[0].Source)
				require.NotNil(t, result.Metrics[0].Offsets)
				require.NotNil(t, result.Metrics[0].States)
				assert.Equal(t, event.PTP_LOCKED, data.GetDataDetails("ens1").State)
				assert.Equal(t, int64(42), data.GetDataDetails("ens1").Offset)
				assert.True(t, cycle.tstamps[0].handled)
				assert.Equal(t, c.GetState(), result.State)
				assert.Equal(t, event.PTP_LOCKED, tc.observation.State)
			})
		}
	}
}

func TestTS2PHCAddEventReportsUnavailableOffset(t *testing.T) {
	for _, clockType := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(clockType), func(t *testing.T) {
			c, _ := newTS2PHCTestClock(clockType)
			first := ts2phcTestStamp("/dev/ptp1", "2000.100", 10)
			assert.Empty(t, c.AddEvent(ts2phcTestEvent(c, "", first)).Events)
			second := ts2phcTestStamp("/dev/ptp1", "2001.100", 11)
			result := c.AddEvent(ts2phcTestEvent(c, "", second))
			for _, report := range result.Events {
				if report.Event.IFace != "ens1" {
					continue
				}
				require.IsType(t, &event.StateData{}, report.Event.Data)
				assert.Equal(t, "master", report.OffsetSource)
				assert.True(t, report.OffsetUnavailable)
				return
			}
			t.Fatal("missing sink offset was not reported")
		})
	}
}

func TestTS2PHCLegacyPollTimeoutPolicy(t *testing.T) {
	for _, clockType := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(clockType), func(t *testing.T) {
			c, tracker := newTS2PHCTestClock(clockType)
			for i := range 3 {
				assert.Empty(t, observeTS2PHCTest(c, "", ts2phcTestStamp(fmt.Sprintf("/dev/ptp%d", i), "2000.100", 10)))
			}
			cycle := tracker.currentPollCycle
			events := observeTS2PHCTest(c, "", &event.TS2PHCObservation{Timestamp: "2000.400", PollTimeout: true})
			require.Len(t, events, 3, "the refactor preserves the previous all-sink timeout behavior")
			assert.Same(t, cycle, tracker.currentPollCycle)
			assert.Equal(t, []string{"ens0", "ens1", "ens2"}, []string{events[0].IFace, events[1].IFace, events[2].IFace})
			for _, ev := range events {
				assert.Equal(t, event.TS2PHC, ev.Source)
				assert.Equal(t, "ts2phc.1.config", ev.CfgName)
				require.IsType(t, &event.OffsetData{}, ev.Data)
				assert.Equal(t, event.PTP_FREERUN, ev.Data.(*event.OffsetData).State)
			}
			for _, ts := range cycle.tstamps {
				assert.True(t, ts.handled)
			}
		})
	}
}

func TestTS2PHCTBCBatchDoesNotPublishIntermediateState(t *testing.T) {
	c, _ := newTS2PHCTestClock(event.TBC)
	tbc := c.(*TBC)
	tbc.isTTSC = true
	tbc.leadingClockData = tbcLeadingClockParams()
	tbc.leadingClockData.leadingInterface = "ens0"
	tbc.syncState.State = event.PTP_FREERUN
	for _, source := range []event.EventSource{event.PTP4l, event.DPLL} {
		data := c.GetData(source)
		data.State = event.PTP_LOCKED
		data.Details = event.DDetails{{IFace: "ens0", State: event.PTP_LOCKED, HasOffset: true, Time: time.Now().UnixMilli()}}
	}
	fillBCDataWindows(tbc, 0)
	observedIPC := &ipcRecorder{}
	tbc.SetIPC(observedIPC.send)

	// In holdover mode the timeout produces failures for both the leading and
	// follower PHCs. The leader alone does not force FREERUN, but the follower does.
	observeTS2PHCTest(c, "", &event.TS2PHCObservation{PHCDevice: "/dev/ptp0", Role: event.TS2PHCSink})
	observeTS2PHCTest(c, "", ts2phcTestStamp("/dev/ptp0", "2000.100", 10))
	observeTS2PHCTest(c, "", ts2phcTestStamp("/dev/ptp1", "2000.200", 10))
	events := observeTS2PHCTest(c, "", &event.TS2PHCObservation{Timestamp: "2000.400", PollTimeout: true})
	require.Len(t, events, 2)
	assert.Equal(t, event.PTP_FREERUN, c.GetState())
	assert.Nil(t, findMessage(observedIPC.messages, ipc.TypePTPState), "one batch must not publish a transient LOCKED state")
}

func TestTS2PHCTimeoutBeforeNextPPSDoesNotFailHealthySinks(t *testing.T) {
	for _, clockType := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(clockType), func(t *testing.T) {
			c, _ := newTS2PHCTestClock(clockType)
			if clockType == event.TBC {
				tbc := c.(*TBC)
				tbc.isTTSC = true
				tbc.leadingClockData = tbcLeadingClockParams()
				tbc.leadingClockData.leadingInterface = "ens0"
				observeTS2PHCTest(c, "", &event.TS2PHCObservation{PHCDevice: "/dev/ptp0", Role: event.TS2PHCSink})
			} else {
				holdover := &event.TS2PHCObservation{PollSuccessful: true, Holdover: true}
				observeTS2PHCTest(c, "ens0", holdover)
			}

			for i := range 3 {
				device := fmt.Sprintf("/dev/ptp%d", i)
				iface := fmt.Sprintf("ens%d", i)
				assert.Empty(t, observeTS2PHCTest(c, "", ts2phcTestStamp(device, "2000.989", 10)))
				require.Len(t, observeTS2PHCTest(c, iface, ts2phcTestOffset(0)), 1)
			}

			// This is the expected collection-window timeout, before the next
			// PPS edge arrives near the end of the next log second.
			assert.Empty(t, observeTS2PHCTest(c, "", &event.TS2PHCObservation{Timestamp: "2001.322", PollTimeout: true}))
			for i := range 3 {
				device := fmt.Sprintf("/dev/ptp%d", i)
				iface := fmt.Sprintf("ens%d", i)
				assert.Empty(t, observeTS2PHCTest(c, "", ts2phcTestStamp(device, "2001.989", 11)))
				require.Len(t, observeTS2PHCTest(c, iface, ts2phcTestOffset(0)), 1)
			}
			data := c.GetData(event.TS2PHC)
			require.Len(t, data.Details, 3)
			for _, detail := range data.Details {
				assert.Equal(t, event.PTP_LOCKED, detail.State, "the healthy next batch must not be preceded by a synthetic failure")
			}
		})
	}
}

func TestTS2PHCRepeatedTimeoutConfirmsMissingPPSBatch(t *testing.T) {
	c, _ := newTS2PHCTestClock(event.GM)
	for i := range 3 {
		device := fmt.Sprintf("/dev/ptp%d", i)
		iface := fmt.Sprintf("ens%d", i)
		observeTS2PHCTest(c, "", ts2phcTestStamp(device, "2000.989", 10))
		observeTS2PHCTest(c, iface, ts2phcTestOffset(0))
	}
	assert.Empty(t, observeTS2PHCTest(c, "", &event.TS2PHCObservation{Timestamp: "2001.322", PollTimeout: true}))
	events := observeTS2PHCTest(c, "", &event.TS2PHCObservation{Timestamp: "2002.322", PollTimeout: true})
	require.Len(t, events, 3, "the next batch is reported missing only after a later timeout confirms it did not arrive")
	for _, ev := range events {
		switch data := ev.Data.(type) {
		case *event.OffsetData:
			assert.Equal(t, event.PTP_FREERUN, data.State)
		case *event.StateData:
			assert.Equal(t, event.PTP_FREERUN, data.State)
		default:
			t.Fatalf("unexpected missing-sink event data type %T", ev.Data)
		}
	}
}

func TestTS2PHCSharedPHCOffsetsAndDuplicates(t *testing.T) {
	c, tracker := newTS2PHCTestClock(event.GM)
	cfg := ts2phcTestConfig()
	cfg.Interfaces = []event.TS2PHCInterface{{Name: "ens1", PHCDevice: "/dev/ptp1"}, {Name: "alias1", PHCDevice: "/dev/ptp1"}}
	c.ConfigureTS2PHC(*cfg)
	for range 2 {
		observeTS2PHCTest(c, "", ts2phcTestStamp("/dev/ptp1", "2000.100", 10))
	}
	require.Len(t, tracker.currentPollCycle.tstamps, 1)
	events := observeTS2PHCTest(c, "alias1", ts2phcTestOffset(0))
	require.Len(t, events, 1)
	assert.Equal(t, "ens1", events[0].IFace)
	assert.True(t, tracker.currentPollCycle.tstamps[0].handled)
	assert.Empty(t, observeTS2PHCTest(c, "", &event.TS2PHCObservation{Timestamp: "2000.400", PollTimeout: true}))
	assert.Empty(t, observeTS2PHCTest(c, "", &event.TS2PHCObservation{Timestamp: "2001.400", PollTimeout: true}),
		"the next PPS edge may still arrive later in the second")
	events = observeTS2PHCTest(c, "", &event.TS2PHCObservation{Timestamp: "2002.400", PollTimeout: true})
	require.Len(t, events, 1, "a missing shared PHC is not reported twice through its aliases")
}

func TestTS2PHCRoleTransitionsAndUnknownDevices(t *testing.T) {
	roles := []event.TS2PHCRole{event.TS2PHCSink, event.TS2PHCSource, event.TS2PHCUnmanaged}
	for _, from := range roles {
		for _, to := range roles {
			t.Run(string(from)+" to "+string(to), func(t *testing.T) {
				c, tracker := newTS2PHCTestClock(event.TBC)
				observeTS2PHCTest(c, "", &event.TS2PHCObservation{PHCDevice: "/dev/ptp1", Role: from})
				observeTS2PHCTest(c, "", ts2phcTestStamp("/dev/ptp1", "2000.100", 10))
				observeTS2PHCTest(c, "", &event.TS2PHCObservation{PHCDevice: "/dev/ptp1", Role: to})
				ts := tracker.currentPollCycle.tstamps[0]
				assert.Equal(t, to, ts.role)
				assert.Equal(t, from != event.TS2PHCSink || to != event.TS2PHCSink, ts.handled)
				observeTS2PHCTest(c, "", ts2phcTestStamp("/dev/ptp1", "2001.100", 11))
				assert.Equal(t, to != event.TS2PHCSink, tracker.currentPollCycle.tstamps[0].handled)
			})
		}
	}
	c, tracker := newTS2PHCTestClock(event.TBC)
	observeTS2PHCTest(c, "", &event.TS2PHCObservation{PHCDevice: "/dev/ptp99", Role: event.TS2PHCSink})
	observeTS2PHCTest(c, "", ts2phcTestStamp("/dev/ptp99", "2000.100", 10))
	assert.NotContains(t, tracker.roles, "/dev/ptp99")
	events := observeTS2PHCTest(c, "", ts2phcTestStamp("/dev/ptp99", "2001.100", 11))
	require.Len(t, events, 3, "the refactor preserves missing-sink reports at a batch boundary")
	assert.Equal(t, []string{"ens0", "ens1", "ens2"}, []string{events[0].IFace, events[1].IFace, events[2].IFace})
	assert.True(t, tracker.currentPollCycle.tstamps[0].handled)
}

func TestTS2PHCReferenceOffsetAndUnselection(t *testing.T) {
	c, _ := newTS2PHCTestClock(event.TBC)
	observeTS2PHCTest(c, "", &event.TS2PHCObservation{PHCDevice: "/dev/ptp0", Role: event.TS2PHCSource})
	observeTS2PHCTest(c, "", &event.TS2PHCObservation{PHCDevice: "/dev/ptp2", Role: event.TS2PHCUnmanaged})
	observeTS2PHCTest(c, "", ts2phcTestStamp("/dev/ptp0", "2000.100", 10))
	observeTS2PHCTest(c, "", ts2phcTestStamp("/dev/ptp1", "2000.200", 11))
	observeTS2PHCTest(c, "", ts2phcTestStamp("/dev/ptp2", "2000.300", 100))
	events := observeTS2PHCTest(c, "", ts2phcTestStamp("/dev/ptp0", "2001.100", 11))
	require.Len(t, events, 1)
	assert.Equal(t, "ens1", events[0].IFace)
	assert.Equal(t, int64(-1_000_000_000), events[0].Data.(*event.OffsetData).Offset)
	observeTS2PHCTest(c, "", &event.TS2PHCObservation{PHCDevice: "/dev/ptp1", Role: event.TS2PHCUnmanaged})
	assert.Nil(t, c.GetData(event.TS2PHC).GetDataDetails("ens1"), "an unselected sink must not leave a permanent FREERUN detail")
}

func TestTS2PHCBatchBoundariesAndLateOffsets(t *testing.T) {
	c, tracker := newTS2PHCTestClock(event.GM)
	observeTS2PHCTest(c, "", ts2phcTestStamp("/dev/ptp0", "900.999", 10))
	cycle := tracker.currentPollCycle
	observeTS2PHCTest(c, "ens0", ts2phcTestOffset(0))
	assert.Empty(t, observeTS2PHCTest(c, "", &event.TS2PHCObservation{Timestamp: "901.000", PollTimeout: true}))
	assert.Empty(t, observeTS2PHCTest(c, "", ts2phcTestStamp("/dev/ptp1", "901.001", 10)))
	assert.Same(t, cycle, tracker.currentPollCycle, "a log-second boundary does not close a PPS batch")
	observeTS2PHCTest(c, "ens1", ts2phcTestOffset(0))
	assert.Empty(t, observeTS2PHCTest(c, "", ts2phcTestStamp("/dev/ptp2", "901.002", 10)))
	observeTS2PHCTest(c, "ens2", ts2phcTestOffset(0))
	events := observeTS2PHCTest(c, "", ts2phcTestStamp("/dev/ptp0", "901.999", 11))
	assert.Empty(t, events, "offsets that arrive after their timestamps complete the batch")
	assert.NotSame(t, cycle, tracker.currentPollCycle, "a repeated PHC with a new hardware timestamp closes the batch")
}

func TestTS2PHCHoldoverPartialBatchAndRepeatedTimeouts(t *testing.T) {
	c, tracker := newTS2PHCTestClock(event.TBC)
	observeTS2PHCTest(c, "", &event.TS2PHCObservation{PHCDevice: "/dev/ptp0", Role: event.TS2PHCSink})
	observeTS2PHCTest(c, "", ts2phcTestStamp("/dev/ptp0", "900.999", 10))
	cycle := tracker.currentPollCycle
	events := observeTS2PHCTest(c, "", &event.TS2PHCObservation{Timestamp: "901.000", PollTimeout: true})
	require.Len(t, events, 1)
	assert.Same(t, cycle, tracker.currentPollCycle)
	observeTS2PHCTest(c, "", ts2phcTestStamp("/dev/ptp1", "901.001", 10))
	events = observeTS2PHCTest(c, "", &event.TS2PHCObservation{Timestamp: "901.200", PollTimeout: true})
	require.Len(t, events, 1)
	assert.Equal(t, "ens1", events[0].IFace)
	assert.Same(t, cycle, tracker.currentPollCycle, "late timestamps still belong to the same batch")
	events = observeTS2PHCTest(c, "", &event.TS2PHCObservation{Timestamp: "902.200", PollTimeout: true})
	require.Len(t, events, 1, "closing the partial batch reports only its missing sink")
	assert.Equal(t, "ens2", events[0].IFace)
	assert.Empty(t, observeTS2PHCTest(c, "", &event.TS2PHCObservation{Timestamp: "902.700", PollTimeout: true}))
	events = observeTS2PHCTest(c, "", &event.TS2PHCObservation{Timestamp: "903.200", PollTimeout: true})
	require.Len(t, events, 3, "a later timeout confirms the next PPS batch was missed")
}

func TestTS2PHCOffsetsOwnThresholdsAndPreserveRawFacts(t *testing.T) {
	for _, clockType := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(clockType), func(t *testing.T) {
			c, _ := newTS2PHCTestClock(clockType)
			raw := ts2phcTestOffset(10000)
			raw.NMEALocked = utils.ToPtr(true)
			events := observeTS2PHCTest(c, "ens1", raw)
			require.Len(t, events, 1)
			assert.Equal(t, event.PTP_FREERUN, events[0].Data.(*event.OffsetData).State)
			assert.Equal(t, event.PTP_LOCKED, raw.State)
			assert.True(t, *raw.NMEALocked, "clock evaluation must not modify the published observation")
			raw = ts2phcTestOffset(20000)
			raw.Holdover, raw.State = true, event.PTP_HOLDOVER
			events = observeTS2PHCTest(c, "ens1", raw)
			assert.Equal(t, event.PTP_HOLDOVER, events[0].Data.(*event.OffsetData).State)
			raw = ts2phcTestOffset(math.NaN())
			raw.State = event.PTP_FREERUN
			events = observeTS2PHCTest(c, "ens1", raw)
			require.IsType(t, &event.StateData{}, events[0].Data)
		})
	}
}

func TestTS2PHCNMEAStatusDoesNotConsumeTimestamp(t *testing.T) {
	c, tracker := newTS2PHCTestClock(event.TBC)
	cfg := ts2phcTestConfig()
	cfg.Interfaces = cfg.Interfaces[1:2]
	c.ConfigureTS2PHC(*cfg)
	observeTS2PHCTest(c, "", ts2phcTestStamp("/dev/ptp1", "2000.100", 10))
	nmea := ts2phcTestOffset(0)
	nmea.PollSuccessful = false
	observeTS2PHCTest(c, "ens1", nmea)
	assert.False(t, tracker.currentPollCycle.tstamps[0].handled)
	events := observeTS2PHCTest(c, "", ts2phcTestStamp("/dev/ptp1", "2001.100", 11))
	require.Len(t, events, 1)
	require.IsType(t, &event.StateData{}, events[0].Data, "a lone timestamp cannot fabricate a measured offset")
}

func TestTS2PHCClockResetConfigurationAndIsolation(t *testing.T) {
	for _, clockType := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(clockType), func(t *testing.T) {
			c, tracker := newTS2PHCTestClock(clockType)
			other, otherTracker := newTS2PHCTestClock(clockType)
			observeTS2PHCTest(c, "", ts2phcTestStamp("/dev/ptp1", "2000.100", 10))
			observeTS2PHCTest(other, "", ts2phcTestStamp("/dev/ptp1", "2000.100", 10))
			observeTS2PHCTest(c, "ens1", &event.TS2PHCObservation{PollSuccessful: true, Holdover: true})
			assert.False(t, otherTracker.offsetHoldover)
			assert.False(t, otherTracker.currentPollCycle.tstamps[0].handled)
			c.Reset()
			assert.Nil(t, tracker.currentPollCycle)
			assert.False(t, tracker.offsetHoldover)
			assert.Empty(t, tracker.selections)
			assert.Len(t, tracker.roles, 3, "clock reset retains configured identities but drops partial batches")
			assert.Empty(t, observeTS2PHCTest(c, "", ts2phcTestStamp("/dev/ptp1", "2001.100", 11)))
			cfg := ts2phcTestConfig()
			cfg.Interfaces = cfg.Interfaces[2:]
			c.ConfigureTS2PHC(*cfg)
			assert.Nil(t, tracker.currentPollCycle)
			assert.Equal(t, map[string]event.TS2PHCRole{"/dev/ptp2": event.TS2PHCSink}, tracker.roles)
			cfg.Interfaces[0].PHCDevice = "/dev/ptp99"
			assert.Equal(t, "/dev/ptp2", tracker.config.Interfaces[0].PHCDevice)
		})
	}
}

func TestTS2PHCInvalidObservationsDoNotStartBatch(t *testing.T) {
	c, tracker := newTS2PHCTestClock(event.GM)
	for _, observation := range []*event.TS2PHCObservation{
		{}, {Timestamp: "invalid", PHCDevice: "/dev/ptp1", PHCTstamp: &event.PHCTimestamp{}},
		{Timestamp: "2000", PHCDevice: "/dev/ptp1"}, {Timestamp: "invalid", PollTimeout: true},
		{PHCDevice: "/dev/ptp1", Role: event.TS2PHCRole("invalid")},
	} {
		assert.Empty(t, observeTS2PHCTest(c, "", observation))
		assert.Nil(t, tracker.currentPollCycle)
	}
	assert.Equal(t, event.TS2PHCSink, tracker.roles["/dev/ptp1"])
	observeTS2PHCTest(c, "", ts2phcTestStamp("/dev/ptp1", "2000.100", 10))
	require.NotNil(t, tracker.currentPollCycle, "a valid observation starts a poll batch")
}

func TestTS2PHCPollGroupFromTimestamp(t *testing.T) {
	for _, tc := range []struct {
		timestamp string
		want      int
		wantError bool
	}{
		{"55339.867", 55339, false}, {"55339", 55339, false}, {"invalid.867", 0, true}, {"", 0, true},
	} {
		t.Run(tc.timestamp, func(t *testing.T) {
			group, err := pollGroupFromTimestamp(tc.timestamp)
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.want, group)
			}
		})
	}
}
