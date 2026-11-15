package daemon

import (
	"fmt"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTS2PHCTestProcess() *ptpProcess {
	InitializeOffsetMaps()
	p := &ptpProcess{
		ExecProcess: ExecProcess{
			name: event.Ts2PhcProcessName, configName: "ts2phc.1.config",
			messageTag: "[ts2phc.1.config]", eventCh: make(chan event.Event, 32),
		},
		clockType: event.GM,
		ifaces: config.IFaces{
			{Name: "ens0", PhcId: "/dev/ptp0", Source: event.PTP4l},
			{Name: "ens1", PhcId: "/dev/ptp1", Source: event.PTP4l},
		},
		logParser:         parser.NewTS2PHCExtractor(),
		ptpClockThreshold: &ptpv1.PtpClockThreshold{MaxOffsetThreshold: 10000},
	}
	p.resetTS2PHCMatches()
	return p
}

func feedTS2PHCLine(p *ptpProcess, timestamp, message string) {
	processWithParser(p, fmt.Sprintf("ts2phc[%s]: [ts2phc.1.config:7] %s", timestamp, message))
}

func TestTS2PHCTimestampNeedsMatchingOffset(t *testing.T) {
	p := newTS2PHCTestProcess()
	feedTS2PHCLine(p, "2000.100", "adding tstamp 1791217457.0 to clock /dev/ptp0")
	assert.Empty(t, p.eventCh, "a timestamp alone is not a failure")

	feedTS2PHCLine(p, "2000.200", "ens0 offset 0 s2 freq +0")
	events := drainEvents(p.eventCh)
	require.Len(t, events, 1)
	require.IsType(t, &event.OffsetData{}, events[0].Data)
	assert.Equal(t, event.PTP_LOCKED, events[0].Data.(*event.OffsetData).State)

	feedTS2PHCLine(p, "2001.100", "adding tstamp 1791217458.0 to clock /dev/ptp0")
	assert.Empty(t, p.eventCh, "the prior timestamp was matched")
	feedTS2PHCLine(p, "2002.100", "adding tstamp 1791217459.0 to clock /dev/ptp0")
	events = drainEvents(p.eventCh)
	require.Len(t, events, 1, "the next timestamp closes the unmatched interval")
	assert.Equal(t, "ens0", events[0].IFace)
	require.IsType(t, &event.StateData{}, events[0].Data)
	assert.Equal(t, event.PTP_FREERUN, events[0].Data.(*event.StateData).State)

	feedTS2PHCLine(p, "2002.200", "ens0 offset 0 s2 freq +0")
	events = drainEvents(p.eventCh)
	require.Len(t, events, 1, "the offset still reports the recovered state")
	require.IsType(t, &event.OffsetData{}, events[0].Data)
	assert.Equal(t, event.PTP_LOCKED, events[0].Data.(*event.OffsetData).State)
	feedTS2PHCLine(p, "2003.100", "adding tstamp 1791217460.0 to clock /dev/ptp0")
	assert.Empty(t, p.eventCh, "the latest timestamp was matched")
}

func TestTS2PHCTimestampsMatchOffsetsPerDevice(t *testing.T) {
	p := newTS2PHCTestProcess()
	feedTS2PHCLine(p, "2000.100", "adding tstamp 1791217457.0 to clock /dev/ptp0")
	feedTS2PHCLine(p, "2000.200", "adding tstamp 1791217457.1 to clock /dev/ptp1")
	feedTS2PHCLine(p, "2000.300", "ens1 offset 0 s2 freq +0")
	drainEvents(p.eventCh)

	feedTS2PHCLine(p, "2001.100", "adding tstamp 1791217458.0 to clock /dev/ptp0")
	events := drainEvents(p.eventCh)
	require.Len(t, events, 1)
	assert.Equal(t, "ens0", events[0].IFace, "ens1's offset must not consume ens0's timestamp")
	require.IsType(t, &event.StateData{}, events[0].Data)
	assert.Equal(t, event.PTP_FREERUN, events[0].Data.(*event.StateData).State)
}

func TestTS2PHCIgnoresTimeoutAndSelectionPrints(t *testing.T) {
	p := newTS2PHCTestProcess()
	feedTS2PHCLine(p, "2000.100", "adding tstamp 1791217457.0 to clock /dev/ptp0")
	feedTS2PHCLine(p, "2000.400", "poll returns zero, no events")
	feedTS2PHCLine(p, "2000.500", "unselecting /dev/ptp0 for synchronization")
	feedTS2PHCLine(p, "2000.600", "selecting /dev/ptp0 as the reference clock")
	assert.Empty(t, p.eventCh, "neither print participates in status evaluation")

	feedTS2PHCLine(p, "2001.100", "adding tstamp 1791217458.0 to clock /dev/ptp0")
	events := drainEvents(p.eventCh)
	require.Len(t, events, 1, "only the unmatched timestamp determines status")
	assert.Equal(t, "ens0", events[0].IFace)
	require.IsType(t, &event.StateData{}, events[0].Data)
}

func TestTS2PHCNMEAStatusDoesNotMatchTimestamp(t *testing.T) {
	p := newTS2PHCTestProcess()
	feedTS2PHCLine(p, "2000.100", "adding tstamp 1791217457.0 to clock /dev/ptp0")
	feedTS2PHCLine(p, "2000.200", "ens0 nmea_status 1 offset 0 s2")
	drainEvents(p.eventCh)
	feedTS2PHCLine(p, "2001.100", "adding tstamp 1791217458.0 to clock /dev/ptp0")
	events := drainEvents(p.eventCh)
	require.Len(t, events, 1, "GNSS status is not a device offset sample")
	assert.Equal(t, "ens0", events[0].IFace)
	require.IsType(t, &event.StateData{}, events[0].Data)
}

func TestTS2PHCIgnoresUnconfiguredPHCs(t *testing.T) {
	p := newTS2PHCTestProcess()
	feedTS2PHCLine(p, "2000.100", "adding tstamp 1791217457.0 to clock /dev/ptp99")
	feedTS2PHCLine(p, "2001.100", "adding tstamp 1791217458.0 to clock /dev/ptp99")
	assert.Empty(t, p.eventCh)
}

func TestTS2PHCProcessEventIgnoresUnrelatedEvents(t *testing.T) {
	p := newTS2PHCTestProcess()
	p.processTS2PhcEvent(nil)
	p.processTS2PhcEvent(&parser.PTPEvent{})
	p.processTS2PhcEvent(&parser.PTPEvent{
		Ts2Phc: &parser.Ts2PhcEvent{Timestamp: "2000.100"},
	})
	assert.Empty(t, p.eventCh)
}

func TestTS2PHCAddedTimestampValidatesAndInitializesState(t *testing.T) {
	p := newTS2PHCTestProcess()
	for _, data := range []*parser.Ts2PhcEvent{
		{},
		{PHCTstamp: &parser.PHCTimestamp{}},
	} {
		_, err := p.handleAddedTstamp(data)
		require.Error(t, err)
	}

	// The state is also lazily initialized for a process that receives a sample
	// before its startup reset has run.
	p.ts2phcMatches.pending = nil
	previous, err := p.handleAddedTstamp(&parser.Ts2PhcEvent{
		PHCDevice: "/dev/ptp0", PHCTstamp: &parser.PHCTimestamp{Seconds: 2000},
	})
	require.NoError(t, err)
	assert.Empty(t, previous)
	assert.Equal(t, "ens0", p.ts2phcMatches.pending["/dev/ptp0"])

	// Event dispatch logs and ignores malformed timestamp events.
	p.processTS2PhcEvent(&parser.PTPEvent{
		Ts2Phc: &parser.Ts2PhcEvent{PHCTstamp: &parser.PHCTimestamp{}},
	})
	assert.Empty(t, p.eventCh)
}
