package clockmgr

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/alias"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTS2PHCManagerTest(t *testing.T, clockType event.ClockType) (*ClockManager, clock.Clock, *prometheus.GaugeVec, *prometheus.GaugeVec) {
	t.Helper()
	offset := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "ts2phc_test_offset"}, []string{"from", nodeLabel, processLabel, "iface"})
	state := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "ts2phc_test_state"}, []string{processLabel, nodeLabel, "iface"})
	m := Init("test-node", nil, offset, state, nil, nil)
	var c clock.Clock
	var err error
	if clockType == event.GM {
		c, err = clock.NewGM(testClockmgrPtp4lConfig, func() int { return 37 }, &pmc.MockClient{})
	} else {
		c, err = clock.NewTBC(testClockmgrPtp4lConfig, func() int { return 37 }, &pmc.MockClient{}, "")
	}
	require.NoError(t, err)
	require.NoError(t, m.AddClock(c))
	require.NoError(t, m.ConfigureClock("ts2phc.0.config", event.TS2PHCConfig{
		LeadingInterface: "ens0",
		Interfaces: []event.TS2PHCInterface{
			{Name: "ens0", PHCDevice: "/dev/ptp0", Source: event.PPS},
			{Name: "ens1", PHCDevice: "/dev/ptp1", Source: event.PPS},
			{Name: "ens2", PHCDevice: "/dev/ptp2", Source: event.PPS},
		},
		Threshold: event.PtpClockThreshold{MaxOffsetThreshold: 100},
	}))
	return m, c, offset, state
}

func ts2phcManagerObservation(clockType event.ClockType, iface string, data *event.TS2PHCObservation) event.Event {
	return event.Event{Source: event.TS2PHC, CfgName: "ts2phc.0.config", IFace: iface, ClockType: clockType, Time: time.Now().UnixMilli(), Data: data}
}

type ts2phcEventRecorder struct {
	clock.Clock
	added []event.Event
}

func (c *ts2phcEventRecorder) AddEvent(ev event.Event) clock.EventResult {
	c.added = append(c.added, ev)
	return c.Clock.AddEvent(ev)
}

func (c *ts2phcEventRecorder) DuringApply(ev event.Event) (bool, bool) {
	if policy, ok := c.Clock.(clock.ApplyingEventPolicy); ok {
		return policy.DuringApply(ev)
	}
	return true, true
}

func TestTS2PHCManagerRoutesObservationsAndRunStart(t *testing.T) {
	for _, clockType := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(clockType), func(t *testing.T) {
			m, c, _, _ := newTS2PHCManagerTest(t, clockType)
			recorder := &ts2phcEventRecorder{Clock: c}
			require.NoError(t, m.AddClock(recorder))

			m.ProcessEvent(ts2phcManagerObservation(clockType, "ens1", &event.TS2PHCObservation{
				PollSuccessful: true, State: event.PTP_LOCKED, Offset: utils.ToPtr(float64(7)),
			}))
			require.Len(t, recorder.added, 1, "raw observations go through the clock's AddEvent")
			assert.IsType(t, &event.TS2PHCObservation{}, recorder.added[0].Data)
			require.NotNil(t, c.GetData(event.TS2PHC).GetDataDetails("ens1"))

			m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: "ts2phc.0.config", IFace: "ens1",
				Data: &event.TS2PHCObservation{PollSuccessful: true, State: event.PTP_FREERUN, Offset: utils.ToPtr(float64(42))}})
			require.Len(t, recorder.added, 2, "the clock receives and rejects an observation from another source")
			assert.Equal(t, event.PTP_LOCKED, c.GetData(event.TS2PHC).GetDataDetails("ens1").State)

			m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: "ts2phc.0.config", Data: &event.TS2PHCRunStarted{}})
			require.Len(t, recorder.added, 3, "the clock receives and rejects a run-start event from another source")
			require.NotNil(t, c.GetData(event.TS2PHC).GetDataDetails("ens1"))

			if clockType == event.TBC {
				m.SetApplying(true)
			}
			m.ProcessEvent(event.Event{Source: event.TS2PHC, CfgName: "ts2phc.0.config", ClockType: clockType, Data: &event.TS2PHCRunStarted{}})
			m.SetApplying(false)
			require.Len(t, recorder.added, 4, "run-start events go through the clock's AddEvent")
			assert.IsType(t, &event.TS2PHCRunStarted{}, recorder.added[3].Data)
			assert.Nil(t, c.GetData(event.TS2PHC).GetDataDetails("ens1"))

			m.ProcessEvent(event.Event{
				Source: event.TS2PHC, CfgName: "ts2phc.0.config", IFace: "ens1", ClockType: clockType,
				Time: time.Now().UnixMilli(), Data: &event.OffsetData{State: event.PTP_LOCKED, Offset: 15},
			})
			require.Len(t, recorder.added, 5)
			assert.IsType(t, &event.OffsetData{}, recorder.added[4].Data)
			require.NotNil(t, c.GetData(event.TS2PHC).GetDataDetails("ens1"))
		})
	}
}

func TestTS2PHCManagerIgnoresInputForUnsupportedClock(t *testing.T) {
	m := Init("test-node", nil, nil, nil, nil, nil)
	c, err := clock.NewBC("ptp4l.9.config", false, event.PtpClockThreshold{})
	require.NoError(t, err)
	require.NoError(t, m.AddClock(c))
	select {
	case <-m.StatsUpdatesCh():
	default:
	}
	before := c.GetState()
	for _, data := range []event.EventData{&event.TS2PHCObservation{}, &event.TS2PHCRunStarted{}} {
		m.ProcessEvent(event.Event{Source: event.TS2PHC, CfgName: "ts2phc.9.config", Data: data})
		assert.Equal(t, before, c.GetState())
		assert.Empty(t, c.ProcessData(), "unsupported inputs must not create process details")
		select {
		case <-m.StatsUpdatesCh():
			t.Fatal("ignored input must not publish a new snapshot")
		default:
		}
	}
}

func TestTS2PHCClockClassMetricUsesClockConfig(t *testing.T) {
	m, c, _, _ := newTS2PHCManagerTest(t, event.GM)
	classes := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "clock_class_config_test"}, []string{processLabel, nodeLabel, configLabel})
	m.clockClassMetric = classes
	m.ProcessEvent(event.Event{
		Source: event.GNSS, CfgName: "ts2phc.0.config", IFace: "ens0", ClockType: event.GM,
		Time: time.Now().UnixMilli(), Data: &event.GNSSData{GPSStatus: 3},
	})
	assert.Equal(t, event.PTP_FREERUN, c.GetState())
	require.Equal(t, 1, testutil.CollectAndCount(classes))
	assert.Equal(t, float64(248), testutil.ToFloat64(classes.WithLabelValues(string(event.PTP4l), "test-node", "ptp4l.0.config")))
}

func TestTS2PHCManagerPublishesClockVerdictsOnly(t *testing.T) {
	for _, clockType := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(clockType), func(t *testing.T) {
			m, c, offset, state := newTS2PHCManagerTest(t, clockType)
			for i := range 3 {
				m.ProcessEvent(ts2phcManagerObservation(clockType, "", &event.TS2PHCObservation{
					Timestamp: "2000.100", PHCDevice: fmt.Sprintf("/dev/ptp%d", i), PHCTstamp: &event.PHCTimestamp{Seconds: 10},
				}))
			}
			assert.Empty(t, c.GetData(event.TS2PHC).Details, "timestamps/configuration must not become state details")
			assert.Equal(t, 3, testutil.CollectAndCount(offset), "expected series remain present even before measurements arrive")
			unavailable := offset.WithLabelValues("master", "test-node", string(event.TS2PHC), alias.GetAlias("ens1"))
			assert.True(t, math.IsNaN(testutil.ToFloat64(unavailable)))
			key := sourceStateKey{clockName: c.ConfigName(), source: event.TS2PHC}
			assert.Empty(t, m.sourceStateIfaces[key])
			m.ProcessEvent(ts2phcManagerObservation(clockType, "ens1", &event.TS2PHCObservation{
				PollSuccessful: true, State: event.PTP_LOCKED, Offset: utils.ToPtr(float64(0)),
			}))
			assert.Equal(t, event.PTP_LOCKED, c.GetData(event.TS2PHC).State)
			m.ProcessEvent(ts2phcManagerObservation(clockType, "", &event.TS2PHCObservation{Timestamp: "2000.400", PollTimeout: true}))
			require.Len(t, c.GetData(event.TS2PHC).Details, 3, "the refactor preserves all-sink timeout monitoring")
			assert.Equal(t, event.PTP_FREERUN, c.GetData(event.TS2PHC).GetDataDetails("ens0").State)
			assert.Len(t, m.sourceStateIfaces[key], 3)
			assert.Equal(t, event.ClockStateLocked, testutil.ToFloat64(state.WithLabelValues(string(event.TS2PHC), "test-node", alias.GetAlias("ens1"))))
			// Rejected role metadata must not overwrite a real sink's last state.
			m.ProcessEvent(ts2phcManagerObservation(clockType, "ens1", &event.TS2PHCObservation{PHCDevice: "/dev/ptp99", Role: event.TS2PHCSource}))
			assert.Equal(t, event.PTP_LOCKED, c.GetData(event.TS2PHC).GetDataDetails("ens1").State)
		})
	}
}

func TestTS2PHCManagerInvalidatesNMEAStatusOnMissingGNSSOffset(t *testing.T) {
	m, c, offset, _ := newTS2PHCManagerTest(t, event.GM)
	require.NoError(t, m.ConfigureClock("ts2phc.0.config", event.TS2PHCConfig{
		LeadingInterface: "ens0",
		Interfaces:       []event.TS2PHCInterface{{Name: "ens0", PHCDevice: "/dev/ptp0", Source: event.GNSS}},
	}))
	for _, source := range []event.EventSource{event.GNSS, event.DPLL} {
		var data event.EventData = &event.GNSSData{GPSStatus: 3}
		if source == event.DPLL {
			data = &event.DPLLData{State: event.PTP_LOCKED, Offset: utils.ToPtr(int64(0))}
		}
		m.ProcessEvent(event.Event{Source: source, CfgName: "ts2phc.0.config", IFace: "ens0", ClockType: event.GM,
			Time: time.Now().UnixMilli(), Data: data})
	}
	m.ProcessEvent(ts2phcManagerObservation(event.GM, "", &event.TS2PHCObservation{
		Timestamp: "2000.989", PHCDevice: "/dev/ptp0", PHCTstamp: &event.PHCTimestamp{Seconds: 10},
	}))
	m.ProcessEvent(ts2phcManagerObservation(event.GM, "ens0", &event.TS2PHCObservation{
		PollSuccessful: true, State: event.PTP_LOCKED, Offset: utils.ToPtr(float64(0)), NMEALocked: utils.ToPtr(true),
	}))
	require.Equal(t, event.PTP_LOCKED, c.GetState())
	nmea := m.registeredGauges[string(event.NMEA_STATUS)]
	require.NotNil(t, nmea)
	nmeaGauge := nmea.WithLabelValues(string(event.TS2PHC), "test-node", string(event.TS2PHC), alias.GetAlias("ens0"))
	assert.Equal(t, float64(1), testutil.ToFloat64(nmeaGauge))
	offsetGauge := offset.WithLabelValues("master", "test-node", string(event.TS2PHC), alias.GetAlias("ens0"))
	assert.Equal(t, float64(0), testutil.ToFloat64(offsetGauge))

	// The first timeout only opens a candidate next cycle. The later timeout
	// confirms the GNSS sink missed its timestamp and reports StateData (no offset).
	m.ProcessEvent(ts2phcManagerObservation(event.GM, "", &event.TS2PHCObservation{Timestamp: "2001.322", PollTimeout: true}))
	assert.Equal(t, float64(1), testutil.ToFloat64(nmeaGauge))
	m.ProcessEvent(ts2phcManagerObservation(event.GM, "", &event.TS2PHCObservation{Timestamp: "2002.322", PollTimeout: true}))
	assert.Equal(t, event.PTP_FREERUN, c.GetState())
	assert.Equal(t, float64(0), testutil.ToFloat64(nmeaGauge), "state-only GNSS failures clear stale NMEA availability")
	assert.True(t, math.IsNaN(testutil.ToFloat64(offsetGauge)), "a missing timestamp invalidates offset without inventing one")
}

func TestTS2PHCManagerPublishesFinalBatchState(t *testing.T) {
	m, c, _, state := newTS2PHCManagerTest(t, event.GM)
	for _, source := range []event.EventSource{event.GNSS, event.DPLL} {
		data := c.GetData(source)
		data.Details = event.DDetails{{IFace: "ens0", State: event.PTP_LOCKED}}
		data.State = event.PTP_LOCKED
	}
	m.ProcessEvent(ts2phcManagerObservation(event.GM, "ens1", &event.TS2PHCObservation{
		PollSuccessful: true, State: event.PTP_LOCKED, Offset: utils.ToPtr(float64(0)),
	}))
	require.Equal(t, event.PTP_LOCKED, c.GetState())
	select {
	case <-m.SyncStatusUpdateCh():
	default:
	}
	for i := range 3 {
		m.ProcessEvent(ts2phcManagerObservation(event.GM, "", &event.TS2PHCObservation{
			Timestamp: "2000.100", PHCDevice: fmt.Sprintf("/dev/ptp%d", i), PHCTstamp: &event.PHCTimestamp{Seconds: 10},
		}))
	}
	m.ProcessEvent(ts2phcManagerObservation(event.GM, "", &event.TS2PHCObservation{Timestamp: "2000.400", PollTimeout: true}))
	assert.Equal(t, event.PTP_FREERUN, c.GetState())
	for i := range 3 {
		iface := fmt.Sprintf("ens%d", i)
		detail := c.GetData(event.TS2PHC).GetDataDetails(iface)
		require.NotNil(t, detail)
		assert.Equal(t, event.PTP_FREERUN, detail.State)
		assert.Equal(t, event.ClockStateFreerun, testutil.ToFloat64(state.WithLabelValues(string(event.TS2PHC), "test-node", alias.GetAlias(iface))))
	}
	select {
	case <-m.SyncStatusUpdateCh():
	default:
		t.Fatal("the final clock-state change must notify sync-status consumers")
	}
	snapshot := SnapshotStatsForTest(m, []process.StatsRequest{{ClockID: "ts2phc.0.config", Source: event.TS2PHC}})
	assert.Equal(t, event.PTP_FREERUN, snapshot.States["ts2phc.0.config"][event.TS2PHC])
}

func TestTS2PHCManagerPublishesCleanupOnlyStateChange(t *testing.T) {
	m, c, _, _ := newTS2PHCManagerTest(t, event.GM)
	for _, source := range []event.EventSource{event.GNSS, event.DPLL} {
		data := c.GetData(source)
		data.Details = event.DDetails{{IFace: "ens0", State: event.PTP_LOCKED}}
		data.State = event.PTP_LOCKED
	}
	for _, sample := range []struct {
		iface  string
		offset float64
	}{{"ens0", 0}, {"ens1", 100}} {
		m.ProcessEvent(ts2phcManagerObservation(event.GM, sample.iface, &event.TS2PHCObservation{
			PollSuccessful: true, State: event.PTP_LOCKED, Offset: &sample.offset,
		}))
	}
	require.Equal(t, event.PTP_FREERUN, c.GetState())
	select {
	case <-m.SyncStatusUpdateCh():
	default:
	}
	m.ProcessEvent(ts2phcManagerObservation(event.GM, "", &event.TS2PHCObservation{PHCDevice: "/dev/ptp1", Role: event.TS2PHCUnmanaged}))
	assert.Equal(t, event.PTP_LOCKED, c.GetState())
	assert.Nil(t, c.GetData(event.TS2PHC).GetDataDetails("ens1"))
	assert.NotContains(t, m.sourceStateIfaces[sourceStateKey{clockName: c.ConfigName(), source: event.TS2PHC}], alias.GetAlias("ens1"))
	select {
	case <-m.SyncStatusUpdateCh():
	default:
		t.Fatal("removing the last fault must notify sync-status consumers")
	}
}

func TestTS2PHCManagerMissingOffsetDoesNotInventMeasurement(t *testing.T) {
	m, c, offset, state := newTS2PHCManagerTest(t, event.TBC)
	for i := range 2 {
		m.ProcessEvent(ts2phcManagerObservation(event.TBC, "", &event.TS2PHCObservation{
			Timestamp: fmt.Sprintf("%d.100", 2000+i), PHCDevice: "/dev/ptp1", PHCTstamp: &event.PHCTimestamp{Seconds: int64(10 + i)},
		}))
	}
	detail := c.GetData(event.TS2PHC).GetDataDetails("ens1")
	require.NotNil(t, detail)
	assert.Equal(t, event.PTP_FREERUN, detail.State)
	assert.False(t, detail.HasOffset)
	assert.Equal(t, 3, testutil.CollectAndCount(offset))
	unavailable := offset.WithLabelValues("master", "test-node", string(event.TS2PHC), alias.GetAlias("ens1"))
	assert.True(t, math.IsNaN(testutil.ToFloat64(unavailable)), "an unavailable measurement stays NaN, never a fabricated integer offset")
	assert.Equal(t, event.ClockStateFreerun, testutil.ToFloat64(state.WithLabelValues(string(event.TS2PHC), "test-node", alias.GetAlias("ens1"))))
	snapshot := SnapshotStatsForTest(m, []process.StatsRequest{{ClockID: "ts2phc.0.config", Source: event.TS2PHC}})
	assert.Equal(t, event.PTP_FREERUN, snapshot.States["ts2phc.0.config"][event.TS2PHC])

	raw := &event.TS2PHCObservation{PollSuccessful: true, State: event.PTP_LOCKED, Offset: utils.ToPtr(float64(100))}
	m.ProcessEvent(ts2phcManagerObservation(event.TBC, "ens1", raw))
	assert.Equal(t, event.PTP_LOCKED, raw.State)
	assert.Equal(t, event.PTP_FREERUN, detail.State, "the Clock owns threshold evaluation")
	assert.Equal(t, float64(100), testutil.ToFloat64(offset.WithLabelValues("master", "test-node", string(event.TS2PHC), alias.GetAlias("ens1"))))

	m.ProcessEvent(ts2phcManagerObservation(event.TBC, "ens1", &event.TS2PHCObservation{PHCDevice: "/dev/ptp1", Role: event.TS2PHCUnmanaged}))
	assert.Nil(t, c.GetData(event.TS2PHC).GetDataDetails("ens1"))
	assert.NotContains(t, m.sourceStateIfaces[sourceStateKey{clockName: c.ConfigName(), source: event.TS2PHC}], alias.GetAlias("ens1"), "inactive sink state gauges follow clock-owned removal")
}

func TestTS2PHCManagerResetClearsPartialBatchAndStateMetrics(t *testing.T) {
	m, c, offset, _ := newTS2PHCManagerTest(t, event.TBC)
	m.ProcessEvent(ts2phcManagerObservation(event.TBC, "ens1", &event.TS2PHCObservation{PollSuccessful: true, State: event.PTP_LOCKED, Offset: utils.ToPtr(float64(0))}))
	series := offset.WithLabelValues("master", "test-node", string(event.TS2PHC), alias.GetAlias("ens1"))
	m.ProcessEvent(ts2phcManagerObservation(event.TBC, "", &event.TS2PHCObservation{
		Timestamp: "2000.100", PHCDevice: "/dev/ptp1", PHCTstamp: &event.PHCTimestamp{Seconds: 10},
	}))
	key := sourceStateKey{clockName: c.ConfigName(), source: event.TS2PHC}
	require.Len(t, m.sourceStateIfaces[key], 1)
	m.ProcessEvent(event.Event{Source: event.TS2PHC, CfgName: "ts2phc.0.config", Reset: true})
	assert.NotContains(t, m.sourceStateIfaces, key)
	assert.Equal(t, 3, testutil.CollectAndCount(offset), "process reset must not delete still-configured measurement series")
	assert.Same(t, series, offset.WithLabelValues("master", "test-node", string(event.TS2PHC), alias.GetAlias("ens1")))
	assert.True(t, math.IsNaN(testutil.ToFloat64(series)), "the previous run's offset is unavailable after reset")
	m.ProcessEvent(ts2phcManagerObservation(event.TBC, "", &event.TS2PHCObservation{
		Timestamp: "2001.100", PHCDevice: "/dev/ptp1", PHCTstamp: &event.PHCTimestamp{Seconds: 11},
	}))
	assert.Empty(t, c.GetData(event.TS2PHC).Details, "restart must not report a sample from the previous run")
}

func TestTS2PHCExpectedOffsetSeriesStartAsNaN(t *testing.T) {
	for _, clockType := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(clockType), func(t *testing.T) {
			m, c, offset, state := newTS2PHCManagerTest(t, clockType)
			expected := []string{"ens0", "ens1", "ens2"}
			require.Equal(t, len(expected), testutil.CollectAndCount(offset))
			for _, iface := range expected {
				key := metricKey{cfgName: "ts2phc.0.config", process: string(event.TS2PHC), iface: alias.GetAlias(iface), dataType: event.OFFSET}
				require.Contains(t, m.metricCache, key)
				assert.True(t, math.IsNaN(testutil.ToFloat64(m.metricCache[key].gauge.With(m.metricCache[key].labels))))
			}
			assert.Equal(t, event.PTP_NOTSET, c.GetState(), "NaN initialization is not a clock-state decision")
			assert.Empty(t, c.GetData(event.TS2PHC).Details, "unavailable metrics must not create integer offset samples")
			assert.Zero(t, testutil.CollectAndCount(state))
		})
	}
}

func TestTS2PHCConfigRetainsSeriesAndDeletesRemovedInterfaces(t *testing.T) {
	m, _, offset, _ := newTS2PHCManagerTest(t, event.TBC)
	m.ProcessEvent(ts2phcManagerObservation(event.TBC, "ens1", &event.TS2PHCObservation{PollSuccessful: true, State: event.PTP_LOCKED, Offset: utils.ToPtr(float64(17))}))
	series := offset.WithLabelValues("master", "test-node", string(event.TS2PHC), alias.GetAlias("ens1"))
	require.Equal(t, float64(17), testutil.ToFloat64(series))
	require.NoError(t, m.ConfigureClock("ts2phc.0.config", event.TS2PHCConfig{
		LeadingInterface: "ens0",
		Interfaces: []event.TS2PHCInterface{
			{Name: "ens0", PHCDevice: "/dev/ptp0", Source: event.PPS},
			{Name: "ens1", PHCDevice: "/dev/ptp1", Source: event.PPS},
			{Name: "ens3", PHCDevice: "/dev/ptp3", Source: event.PPS},
		},
	}))
	assert.Equal(t, 3, testutil.CollectAndCount(offset))
	assert.Same(t, series, offset.WithLabelValues("master", "test-node", string(event.TS2PHC), alias.GetAlias("ens1")), "retained series must not be deleted and recreated")
	assert.True(t, math.IsNaN(testutil.ToFloat64(series)))
	assert.NotContains(t, m.metricCache, metricKey{cfgName: "ts2phc.0.config", process: string(event.TS2PHC), iface: alias.GetAlias("ens2"), dataType: event.OFFSET})
	assert.Contains(t, m.metricCache, metricKey{cfgName: "ts2phc.0.config", process: string(event.TS2PHC), iface: alias.GetAlias("ens3"), dataType: event.OFFSET})
	newSeries := offset.WithLabelValues("master", "test-node", string(event.TS2PHC), alias.GetAlias("ens3"))
	assert.True(t, math.IsNaN(testutil.ToFloat64(newSeries)))
	m.ProcessEvent(ts2phcManagerObservation(event.TBC, "ens1", &event.TS2PHCObservation{PollSuccessful: true, State: event.PTP_LOCKED, Offset: utils.ToPtr(float64(0))}))
	assert.Zero(t, testutil.ToFloat64(series), "a genuine zero replaces NaN normally")
}

func TestTS2PHCUnavailableMeasurementInvalidatesLastValue(t *testing.T) {
	m, c, offset, _ := newTS2PHCManagerTest(t, event.TBC)
	for _, value := range []float64{11, 17} {
		m.ProcessEvent(ts2phcManagerObservation(event.TBC, "ens1", &event.TS2PHCObservation{PollSuccessful: true, State: event.PTP_LOCKED, Offset: &value}))
	}
	series := offset.WithLabelValues("master", "test-node", string(event.TS2PHC), alias.GetAlias("ens1"))
	for i := range 2 {
		m.ProcessEvent(ts2phcManagerObservation(event.TBC, "", &event.TS2PHCObservation{
			Timestamp: fmt.Sprintf("%d.100", 2000+i), PHCDevice: "/dev/ptp1", PHCTstamp: &event.PHCTimestamp{Seconds: int64(10 + i)},
		}))
	}
	assert.True(t, math.IsNaN(testutil.ToFloat64(series)), "a state-only missing-offset failure must not leave the previous measurement visible as current")
	assert.Equal(t, event.PTP_FREERUN, c.GetData(event.TS2PHC).GetDataDetails("ens1").State)
	assert.Equal(t, int64(17), c.GetData(event.TS2PHC).GetDataDetails("ens1").Offset, "the last-known integer sample remains separate from metric availability")
	m.ProcessEvent(ts2phcManagerObservation(event.TBC, "ens1", &event.TS2PHCObservation{PollSuccessful: true, State: event.PTP_LOCKED, Offset: utils.ToPtr(float64(23))}))
	assert.Equal(t, float64(23), testutil.ToFloat64(series))
}

func TestTS2PHCOffsetAvailabilityFollowsClockPolicy(t *testing.T) {
	m, _, offset, _ := newTS2PHCManagerTest(t, event.TBC)
	assert.Equal(t, 3, testutil.CollectAndCount(offset), "all configured sinks start monitored")
	m.ProcessEvent(ts2phcManagerObservation(event.TBC, "", &event.TS2PHCObservation{PHCDevice: "/dev/ptp0", Role: event.TS2PHCSink}))
	assert.Equal(t, 3, testutil.CollectAndCount(offset), "the leading sink remains monitored")
	leadingSeries := offset.WithLabelValues("master", "test-node", string(event.TS2PHC), alias.GetAlias("ens0"))
	assert.True(t, math.IsNaN(testutil.ToFloat64(leadingSeries)))
	m.ProcessEvent(ts2phcManagerObservation(event.TBC, "", &event.TS2PHCObservation{PHCDevice: "/dev/ptp0", Role: event.TS2PHCUnmanaged}))
	assert.Equal(t, 2, testutil.CollectAndCount(offset), "an unselected PHC is no longer an expected measurement")
	m.RemoveAllClocks()
	assert.Zero(t, testutil.CollectAndCount(offset), "removed profiles delete their measurement series")
}

func TestTS2PHCManagerKeepsRunBoundaryDuringApply(t *testing.T) {
	m, c, _, _ := newTS2PHCManagerTest(t, event.TBC)
	// The old process ends during apply; preserve its pending batch until the
	// new run signal, while dropping ordinary runtime observations.
	m.ProcessEvent(ts2phcManagerObservation(event.TBC, "", &event.TS2PHCObservation{
		Timestamp: "1998.100", PHCDevice: "/dev/ptp1", PHCTstamp: &event.PHCTimestamp{Seconds: 8},
	}))
	m.SetApplying(true)
	m.ProcessEvent(event.Event{Source: event.TS2PHC, CfgName: "ts2phc.0.config", ClockType: event.TBC, Data: &event.TS2PHCRunStarted{}})
	m.ProcessEvent(ts2phcManagerObservation(event.TBC, "", &event.TS2PHCObservation{
		Timestamp: "1999.100", PHCDevice: "/dev/ptp1", PHCTstamp: &event.PHCTimestamp{Seconds: 9},
	}))
	m.SetApplying(false)
	m.ProcessEvent(ts2phcManagerObservation(event.TBC, "", &event.TS2PHCObservation{
		Timestamp: "2000.100", PHCDevice: "/dev/ptp1", PHCTstamp: &event.PHCTimestamp{Seconds: 10},
	}))
	assert.Empty(t, c.GetData(event.TS2PHC).Details, "the run boundary clears the old batch and runtime observations stay suppressed during apply")
	m.ProcessEvent(ts2phcManagerObservation(event.TBC, "", &event.TS2PHCObservation{
		Timestamp: "2001.100", PHCDevice: "/dev/ptp1", PHCTstamp: &event.PHCTimestamp{Seconds: 11},
	}))
	detail := c.GetData(event.TS2PHC).GetDataDetails("ens1")
	require.NotNil(t, detail, "setup configuration remains available after apply")
	assert.Equal(t, event.PTP_FREERUN, detail.State)
}

func TestTS2PHCManagerRunStartPreservesOtherSourcesAndInvalidatesMeasurements(t *testing.T) {
	for _, clockType := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(clockType), func(t *testing.T) {
			m, c, offset, _ := newTS2PHCManagerTest(t, clockType)
			// No aggregate Clock evaluation is needed to set up independent facts.
			dpll := c.GetData(event.DPLL)
			dpll.Details = append(dpll.Details, &event.DataDetails{IFace: "ens0", State: event.PTP_LOCKED})
			dpll.State = event.PTP_LOCKED
			m.ProcessEvent(ts2phcManagerObservation(clockType, "ens1", &event.TS2PHCObservation{
				PollSuccessful: true, State: event.PTP_LOCKED, Offset: utils.ToPtr(float64(7)),
			}))
			series := offset.WithLabelValues("master", "test-node", string(event.TS2PHC), alias.GetAlias("ens1"))
			require.Equal(t, float64(7), testutil.ToFloat64(series))
			m.ProcessEvent(ts2phcManagerObservation(clockType, "", &event.TS2PHCObservation{
				Timestamp: "2000.100", PHCDevice: "/dev/ptp1", PHCTstamp: &event.PHCTimestamp{Seconds: 10},
			}))
			m.ProcessEvent(event.Event{Source: event.TS2PHC, CfgName: "ts2phc.0.config", ClockType: clockType, Data: &event.TS2PHCRunStarted{}})
			assert.True(t, math.IsNaN(testutil.ToFloat64(series)), "new run invalidates the previous run's measurement")
			assert.Same(t, dpll, c.GetData(event.DPLL), "new run does not reset DPLL or other independent clock facts")
			assert.Equal(t, event.PTP_LOCKED, dpll.State)
			assert.Empty(t, c.GetData(event.TS2PHC).Details)
			m.ProcessEvent(ts2phcManagerObservation(clockType, "", &event.TS2PHCObservation{
				Timestamp: "2001.100", PHCDevice: "/dev/ptp1", PHCTstamp: &event.PHCTimestamp{Seconds: 11},
			}))
			assert.Empty(t, c.GetData(event.TS2PHC).Details, "partial timestamps never cross process runs")
		})
	}
}

func TestTS2PHCManagerGMRunStartPublishesStateChange(t *testing.T) {
	m, c, _, _ := newTS2PHCManagerTest(t, event.GM)
	for _, source := range []event.EventSource{event.GNSS, event.DPLL} {
		data := c.GetData(source)
		data.Details = event.DDetails{{IFace: "ens0", State: event.PTP_LOCKED}}
		data.State = event.PTP_LOCKED
	}
	m.ProcessEvent(ts2phcManagerObservation(event.GM, "ens1", &event.TS2PHCObservation{
		PollSuccessful: true, State: event.PTP_LOCKED, Offset: utils.ToPtr(float64(0)),
	}))
	require.Equal(t, event.PTP_LOCKED, c.GetState())
	select {
	case <-m.SyncStatusUpdateCh():
	default:
	}
	m.ProcessEvent(event.Event{Source: event.TS2PHC, CfgName: "ts2phc.0.config", ClockType: event.GM, Data: &event.TS2PHCRunStarted{}})
	assert.Equal(t, event.PTP_FREERUN, c.GetState())
	select {
	case <-m.SyncStatusUpdateCh():
	default:
		t.Fatal("a GM state change at run start must notify sync-status consumers")
	}
	assert.Equal(t, event.PTP_FREERUN, m.osClock.E3State, "run-start evaluation updates the derived OS-clock state")
}

func TestTS2PHCManagerSetupConfigurationDoesNotActAsSample(t *testing.T) {
	m, c, offset, _ := newTS2PHCManagerTest(t, event.TBC)
	m.SetApplying(true)
	dpll := c.GetData(event.DPLL)
	dpll.Details = event.DDetails{{IFace: "ens0", State: event.PTP_LOCKED}}
	dpll.State = event.PTP_LOCKED
	beforeState := c.GetState()
	require.NoError(t, m.ConfigureClock("ts2phc.0.config", event.TS2PHCConfig{
		LeadingInterface: "ens0",
		Interfaces:       []event.TS2PHCInterface{{Name: "ens9", PHCDevice: "/dev/ptp9"}},
	}))
	assert.Equal(t, beforeState, c.GetState())
	assert.Same(t, dpll, c.GetData(event.DPLL))
	assert.Nil(t, c.GetData(event.TS2PHC).GetDataDetails("ens9"), "setup must not create a clock observation")
	assert.Equal(t, 1, testutil.CollectAndCount(offset))
	series := offset.WithLabelValues("master", "test-node", string(event.TS2PHC), alias.GetAlias("ens9"))
	assert.True(t, math.IsNaN(testutil.ToFloat64(series)))
}

func TestTS2PHCManagerConfigureUnknownClock(t *testing.T) {
	m := Init("test-node", nil, nil, nil, nil, nil)
	require.ErrorContains(t, m.ConfigureClock("ts2phc.9.config", event.TS2PHCConfig{}), "does not support configuration")
}

func TestClockManagerRejectsUnsupportedConfigurationWithoutMutation(t *testing.T) {
	m, c, offset, _ := newTS2PHCManagerTest(t, event.TBC)
	m.ProcessEvent(ts2phcManagerObservation(event.TBC, "ens1", &event.TS2PHCObservation{
		PollSuccessful: true, State: event.PTP_LOCKED, Offset: utils.ToPtr(float64(17)),
	}))
	series := offset.WithLabelValues("master", "test-node", string(event.TS2PHC), alias.GetAlias("ens1"))
	require.Equal(t, float64(17), testutil.ToFloat64(series))
	before := c.GetState()
	require.ErrorContains(t, m.ConfigureClock("ts2phc.0.config", nil), "unsupported clock configuration")
	assert.Equal(t, before, c.GetState())
	assert.Equal(t, float64(17), testutil.ToFloat64(series))
	assert.NotNil(t, c.GetData(event.TS2PHC).GetDataDetails("ens1"))
	var nilConfig *event.TS2PHCConfig
	require.ErrorContains(t, m.ConfigureClock("ts2phc.0.config", nilConfig), "unsupported clock configuration")
	assert.Equal(t, float64(17), testutil.ToFloat64(series))
}

func TestClockManagerConfiguresClockFromPointer(t *testing.T) {
	m, c, offset, _ := newTS2PHCManagerTest(t, event.TBC)
	config := &event.TS2PHCConfig{
		LeadingInterface: "ens0",
		Interfaces:       []event.TS2PHCInterface{{Name: "ens9", PHCDevice: "/dev/ptp9"}},
	}
	require.NoError(t, m.ConfigureClock("ts2phc.0.config", config))
	assert.Equal(t, event.PTP_NOTSET, c.GetState())
	require.Equal(t, 1, testutil.CollectAndCount(offset))
	assert.True(t, math.IsNaN(testutil.ToFloat64(offset.WithLabelValues("master", "test-node", string(event.TS2PHC), alias.GetAlias("ens9")))))
}

func TestTS2PHCRunBoundaryKeepsThresholdAndConfiguredInterfaces(t *testing.T) {
	m, c, _, _ := newTS2PHCManagerTest(t, event.TBC)
	m.ProcessEvent(event.Event{Source: event.TS2PHC, CfgName: "ts2phc.0.config", ClockType: event.TBC, Data: &event.TS2PHCRunStarted{}})
	m.ProcessEvent(ts2phcManagerObservation(event.TBC, "ens1", &event.TS2PHCObservation{
		PollSuccessful: true, State: event.PTP_LOCKED, Offset: utils.ToPtr(float64(100)),
	}))
	detail := c.GetData(event.TS2PHC).GetDataDetails("ens1")
	require.NotNil(t, detail)
	assert.Equal(t, event.PTP_FREERUN, detail.State, "the configured threshold survives the run reset")
}
