package clockmgr

import (
	"io"
	"os"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/alias"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

type reportingTestClock struct {
	*clock.BCClock
	result clock.EventResult
	calls  int
}

func (c *reportingTestClock) AddEvent(ev event.Event) clock.EventResult {
	c.calls++
	c.result.SyncState = c.BCClock.AddEvent(ev).SyncState
	return c.result
}

func newReportingOffsetGauge(name string) *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: name}, []string{"from", nodeLabel, processLabel, holdoverInterfaceLabel})
}

func TestProcessEventGenericReporting(t *testing.T) {
	for _, sample := range []struct {
		name     string
		override bool
		suppress bool
	}{
		{name: "passthrough"},
		{name: "non-DPLL override", override: true},
		{name: "suppression", suppress: true},
		{name: "suppression takes precedence", override: true, suppress: true},
	} {
		t.Run(sample.name, func(t *testing.T) {
			gauge := newReportingOffsetGauge("test_generic_report_offset")
			manager := Init("test-node", make(chan event.Event, 16), gauge, nil, nil, nil)
			t.Cleanup(manager.RemoveAllClocks)
			bc, err := clock.NewBC(testClockmgrPtp4lConfig, false, event.PtpClockThreshold{})
			require.NoError(t, err)
			receiver := &reportingTestClock{BCClock: bc}
			require.NoError(t, manager.AddClock(receiver))
			raw := &event.OffsetData{State: event.PTP_LOCKED, Offset: 3}
			ev := event.Event{CfgName: testClockmgrPtp4lConfig, IFace: "ens2f0", Source: event.PTP4l, ClockType: event.BC, Time: 1, Data: raw, WriteToLog: true}
			reported := ev
			if sample.override {
				reported.Source = event.TS2PHC
				reported.Data = &event.OffsetData{State: event.PTP_HOLDOVER, Offset: 42}
				receiver.result.Report = &reported
			}
			receiver.result.SuppressReport = sample.suppress
			reader, writer, err := os.Pipe()
			require.NoError(t, err)
			stdout := os.Stdout
			os.Stdout = writer
			func() {
				defer func() { os.Stdout = stdout }()
				manager.ProcessEvent(ev)
			}()
			require.NoError(t, writer.Close())
			output, err := io.ReadAll(reader)
			require.NoError(t, err)
			require.NoError(t, reader.Close())
			require.Equal(t, 1, receiver.calls)
			require.Equal(t, int64(3), raw.Offset)
			stored := receiver.GetData(event.PTP4l).GetDataDetails(ev.IFace)
			require.Contains(t, stored.LogData, raw.String())
			require.Equal(t, event.PTP_LOCKED, stored.State)
			if sample.suppress {
				require.Empty(t, output)
				require.Empty(t, manager.metricCache)
				return
			}
			require.Contains(t, string(output), reported.Data.String())
			expected := float64(3)
			from := string(reported.Source)
			if sample.override {
				expected = 42
				from = "master"
			}
			require.Equal(t, expected, testutil.ToFloat64(gauge.WithLabelValues(from, "test-node", string(reported.Source), alias.GetAlias(ev.IFace))))
		})
	}
}
