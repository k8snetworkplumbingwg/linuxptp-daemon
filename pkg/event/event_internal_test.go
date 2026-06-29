// # Assisted by watsonx Code Assistant
package event

import (
	"sync"
	"testing"
	"time"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type MockData struct {
	Data map[string][]*Data
}

type noopClockIO struct{ sync.Mutex }

func (n *noopClockIO) emitClockClass(fbprotocol.ClockClass, string) {}
func (n *noopClockIO) sendIPC(ipc.Message)                          {}
func (n *noopClockIO) getGMSettings(string) (protocol.GrandmasterSettings, error) {
	return protocol.GrandmasterSettings{}, nil
}
func (n *noopClockIO) setGMSettings(string, protocol.GrandmasterSettings) error { return nil }
func (n *noopClockIO) setExternalGMPropertiesNP(string, protocol.ExternalGrandmasterProperties) error {
	return nil
}
func (n *noopClockIO) getParentTimeAndCurrentDS(string) (pmc.ParentTimeCurrentDS, error) {
	return pmc.ParentTimeCurrentDS{}, nil
}
func (n *noopClockIO) getUtcOffset() int { return 37 }

func newTestTBCClock(lcp *LeadingClockParams) *TBCClock {
	if lcp == nil {
		lcp = newLeadingClockParams()
	}
	return &TBCClock{
		io:               &noopClockIO{},
		leadingClockData: lcp,
		syncState: clockSyncState{
			state:         PTP_FREERUN,
			clockClass:    protocol.ClockClassUninitialized,
			clockAccuracy: fbprotocol.ClockAccuracyUnknown,
		},
		overallSyncState: PTP_FREERUN,
		osClockState:     PTP_NOTSET,
	}
}

func TestAddEvent_NormalizesCfgName(t *testing.T) {
	t.Run("PTP4l event gets cfgName from TBCClock", func(t *testing.T) {
		bc := newTestTBCClock(nil)
		bc.cfgName = testTS2PHCCfg
		ev := Event{
			Source:  PTP4lProcessName,
			CfgName: testPTP4lCfg,
			IFace:   testEth0,
		}
		ev, _, _ = bc.addEvent(ev)
		assert.Equal(t, testTS2PHCCfg, ev.CfgName)
	})

	t.Run("Non-PTP4l event keeps original cfgName", func(t *testing.T) {
		bc := newTestTBCClock(nil)
		bc.cfgName = testTS2PHCCfg
		ev := Event{
			Source:  DPLL,
			CfgName: testTS2PHCCfg,
			IFace:   testEth0,
		}
		ev, _, _ = bc.addEvent(ev)
		assert.Equal(t, testTS2PHCCfg, ev.CfgName)
	})
}

const (
	testConfig      = "config"
	testIface       = "iface"
	testEth0        = "eth0"
	testEth1        = "eth1"
	testEth2        = "eth2"
	testEth99       = "eth99"
	testIFace1      = "IFace1"
	testIface1Lower = "iface1"
	testEno8703     = "eno8703"
	testEns7f0      = "ens7f0"
	testPTP4lCfg    = "ptp4l.0.config"
	testTS2PHCCfg   = "ts2phc.0.config"
)

func TestUpdateLeadingClockData_PTP4lProcessName(t *testing.T) {
	ev := Event{
		Source: PTP4lProcessName,
		Data: &PTPData{
			Values: map[ValueType]interface{}{
				ControlledPortsConfig: testConfig,
				ClockIDKey:            "clockID",
			},
		},
	}

	bc := newTestTBCClock(&LeadingClockParams{})
	bc.updateLeadingClockData(ev)

	assert.Equal(t, testConfig, bc.leadingClockData.controlledPortsConfig)
	assert.Equal(t, "clockID", bc.leadingClockData.clockID)
}

func TestUpdateLeadingClockData_DPLL(t *testing.T) {
	ev := Event{
		Source: DPLL,
		IFace:  testIface,
		Data: &PTPData{
			Values: map[ValueType]interface{}{
				LeadingSource:            true,
				InSyncConditionThreshold: uint64(100),
				InSyncConditionTimes:     uint64(200),
				ToFreeRunThreshold:       uint64(300),
				MaxInSpecOffset:          uint64(400),
			},
		},
	}

	bc := newTestTBCClock(&LeadingClockParams{})
	bc.updateLeadingClockData(ev)

	assert.Equal(t, testIface, bc.leadingClockData.leadingInterface)
	assert.Equal(t, 100, bc.leadingClockData.inSyncConditionThreshold)
	assert.Equal(t, 200, bc.leadingClockData.inSyncConditionTimes)
	assert.Equal(t, 300, bc.leadingClockData.toFreeRunThreshold)
	assert.Equal(t, uint64(400), bc.leadingClockData.MaxInSpecOffset)
}

func TestGetLeadingInterfaceBC(t *testing.T) {
	tests := []struct {
		name     string
		lcp      *LeadingClockParams
		expected string
	}{
		{
			name:     "LeadingInterface is not empty",
			lcp:      &LeadingClockParams{leadingInterface: testEth0},
			expected: testEth0,
		},
		{
			name:     "LeadingInterface is empty",
			lcp:      &LeadingClockParams{},
			expected: LEADING_INTERFACE_UNKNOWN,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bc := newTestTBCClock(tt.lcp)
			result := bc.getLeadingInterfaceBC()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestInSpecCondition(t *testing.T) {
	t.Run("returns false when MaxInSpecOffset is 0", func(t *testing.T) {
		bc := newTestTBCClock(&LeadingClockParams{MaxInSpecOffset: 0})
		result := bc.inSpecCondition()
		assert.False(t, result)
	})

	t.Run("returns false when offset is out of spec", func(t *testing.T) {
		bc := newTestTBCClock(&LeadingClockParams{MaxInSpecOffset: 5})
		bc.data = []*Data{
			{ProcessName: DPLL, Details: []*DataDetails{{IFace: testIface1Lower, Offset: 10}}},
		}
		bc.syncState.leadingIFace = testIface1Lower
		result := bc.inSpecCondition()
		assert.False(t, result)
	})

	t.Run("returns true when offset is in spec", func(t *testing.T) {
		bc := newTestTBCClock(&LeadingClockParams{MaxInSpecOffset: 5})
		bc.data = []*Data{
			{ProcessName: DPLL, Details: []*DataDetails{{IFace: testIface1Lower, Offset: 3}}},
		}
		bc.syncState.leadingIFace = testIface1Lower
		result := bc.inSpecCondition()
		assert.True(t, result)
	})
}

func TestFreeRunCondition(t *testing.T) {
	tests := []struct {
		name         string
		data         []*Data
		leadingIFace string
		threshold    int
		fillWindow   bool
		expected     bool
	}{
		{
			name:         "Free run condition not met",
			data:         []*Data{{ProcessName: DPLL, Details: []*DataDetails{{IFace: testIFace1, Offset: 5}}}},
			leadingIFace: testIFace1,
			threshold:    10,
			expected:     false,
		},
		{
			name:         "Free run condition met",
			data:         []*Data{{ProcessName: DPLL, Details: []*DataDetails{{IFace: testIFace1, Offset: 15}}}},
			leadingIFace: testIFace1,
			threshold:    10,
			expected:     true,
		},
		{
			name:         "Free run condition pending initialization",
			data:         nil,
			leadingIFace: testIFace1,
			threshold:    0,
			expected:     true,
		},
		{
			name: "Free run condition met via PTP4l window mean",
			data: []*Data{
				{ProcessName: DPLL, Details: []*DataDetails{{IFace: testIFace1, Offset: 5}}},
				{ProcessName: PTP4l, Details: []*DataDetails{{IFace: testIFace1, Offset: 9798319463}}},
			},
			leadingIFace: testIFace1,
			threshold:    1500,
			fillWindow:   true,
			expected:     true,
		},
		{
			name: "PTP4l window mean below threshold does not trigger free run",
			data: []*Data{
				{ProcessName: DPLL, Details: []*DataDetails{{IFace: testIFace1, Offset: 5}}},
				{ProcessName: PTP4l, Details: []*DataDetails{{IFace: testIFace1, Offset: 100}}},
			},
			leadingIFace: testIFace1,
			threshold:    1500,
			fillWindow:   true,
			expected:     false,
		},
		{
			name: "PTP4l with empty window is skipped",
			data: []*Data{
				{ProcessName: DPLL, Details: []*DataDetails{{IFace: testIFace1, Offset: 5}}},
				{ProcessName: PTP4l, Details: []*DataDetails{{IFace: testIFace1, Offset: 9798319463}}},
			},
			leadingIFace: testIFace1,
			threshold:    1500,
			expected:     false,
		},
		{
			name: "PTP4l stale detail on leading iface does not trigger free run when window has good data",
			data: []*Data{
				{ProcessName: DPLL, Details: []*DataDetails{{IFace: testEno8703, Offset: 0}}},
				{ProcessName: PTP4l, Details: []*DataDetails{
					{IFace: testEno8703, Offset: -93},
					{IFace: "eno8903", Offset: 2},
				}},
			},
			leadingIFace: testEno8703,
			threshold:    30,
			fillWindow:   true,
			expected:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.fillWindow {
				for _, d := range tt.data {
					if d.ProcessName == PTP4l && len(d.Details) > 0 {
						d.window = *utils.NewWindow(WindowSize)
						lastOffset := d.Details[len(d.Details)-1].Offset
						for i := 0; i < WindowSize; i++ {
							d.window.Insert(float64(lastOffset))
						}
					}
				}
			}
			bc := newTestTBCClock(&LeadingClockParams{toFreeRunThreshold: tt.threshold})
			bc.data = tt.data
			bc.syncState.leadingIFace = tt.leadingIFace

			result := bc.freeRunCondition()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetLargestOffset(t *testing.T) {
	currentTime := time.Now().Unix()
	staleTime := (currentTime - StaleEventAfter) * 1000
	recentTime := currentTime * 1000

	tests := []struct {
		name         string
		data         []*Data
		leadingIFace string
		fillWindow   bool
		expected     int64
	}{
		{
			name:     "No data for config",
			data:     nil,
			expected: FaultyPhaseOffset,
		},
		{
			name:     "No process data",
			data:     []*Data{},
			expected: FaultyPhaseOffset,
		},
		{
			name: "No details in data",
			data: []*Data{
				{ProcessName: DPLL, Details: []*DataDetails{}},
				{ProcessName: "other", Details: []*DataDetails{}},
			},
			leadingIFace: testEth99,
			expected:     FaultyPhaseOffset,
		},
		{
			name: "Window not full - should return FaultyPhaseOffset",
			data: []*Data{
				{ProcessName: DPLL, Details: []*DataDetails{
					{IFace: testEth0, Offset: 15, time: recentTime},
					{IFace: testEth1, Offset: 5, time: recentTime},
				}},
			},
			leadingIFace: testEth99,
			expected:     FaultyPhaseOffset,
		},
		{
			name: "Single offset value",
			data: []*Data{
				{ProcessName: DPLL, Details: []*DataDetails{
					{IFace: testEth0, Offset: 100, time: recentTime},
				}},
			},
			leadingIFace: testEth99,
			fillWindow:   true,
			expected:     100,
		},
		{
			name: "Multiple offsets - largest positive",
			data: []*Data{
				{ProcessName: DPLL, Details: []*DataDetails{
					{IFace: testEth0, Offset: 15, time: recentTime},
					{IFace: testEth1, Offset: 5, time: recentTime},
					{IFace: testEth2, Offset: 25, time: recentTime},
				}},
			},
			leadingIFace: testEth99,
			fillWindow:   true,
			expected:     25,
		},
		{
			name: "Multiple offsets - largest negative",
			data: []*Data{
				{ProcessName: DPLL, Details: []*DataDetails{
					{IFace: testEth0, Offset: -30, time: recentTime},
					{IFace: testEth1, Offset: 5, time: recentTime},
					{IFace: testEth2, Offset: -10, time: recentTime},
				}},
			},
			leadingIFace: testEth99,
			fillWindow:   true,
			expected:     -30,
		},
		{
			name: "Mixed positive and negative - largest absolute value",
			data: []*Data{
				{ProcessName: DPLL, Details: []*DataDetails{
					{IFace: testEth0, Offset: 20, time: recentTime},
					{IFace: testEth1, Offset: -25, time: recentTime},
					{IFace: testEth2, Offset: 15, time: recentTime},
				}},
			},
			leadingIFace: testEth99,
			fillWindow:   true,
			expected:     -25,
		},
		{
			name: "Stale data filtered out",
			data: []*Data{
				{ProcessName: DPLL, Details: []*DataDetails{
					{IFace: testEth0, Offset: 100, time: staleTime - 1000},
					{IFace: testEth1, Offset: 20, time: recentTime},
					{IFace: testEth2, Offset: 50, time: staleTime - 500},
				}},
			},
			leadingIFace: testEth99,
			fillWindow:   true,
			expected:     20,
		},
		{
			name: "All data stale - should return FaultyPhaseOffset",
			data: []*Data{
				{ProcessName: DPLL, Details: []*DataDetails{
					{IFace: testEth0, Offset: 15, time: staleTime - 1000},
					{IFace: testEth1, Offset: 50, time: staleTime - 500},
					{IFace: testEth2, Offset: 30, time: staleTime - 200},
				}},
			},
			leadingIFace: testEth99,
			expected:     FaultyPhaseOffset,
		},
		{
			name: "Multiple processes with different offsets",
			data: []*Data{
				{ProcessName: DPLL, Details: []*DataDetails{
					{IFace: testEth0, Offset: 20, time: recentTime},
				}},
				{ProcessName: "ptp4l", Details: []*DataDetails{
					{IFace: testEth1, Offset: 35, time: recentTime},
				}},
				{ProcessName: "ts2phc", Details: []*DataDetails{
					{IFace: testEth2, Offset: -40, time: recentTime},
				}},
			},
			leadingIFace: testEth99,
			fillWindow:   true,
			expected:     -40,
		},
		{
			name: "Zero offset values",
			data: []*Data{
				{ProcessName: DPLL, Details: []*DataDetails{
					{IFace: testEth0, Offset: 0, time: recentTime},
					{IFace: testEth1, Offset: 0, time: recentTime},
				}},
			},
			leadingIFace: testEth99,
			fillWindow:   true,
			expected:     0,
		},
		{
			name: "Mix of zero and non-zero offsets",
			data: []*Data{
				{ProcessName: DPLL, Details: []*DataDetails{
					{IFace: testEth0, Offset: 0, time: recentTime},
					{IFace: testEth1, Offset: 10, time: recentTime},
					{IFace: testEth2, Offset: 0, time: recentTime},
				}},
			},
			leadingIFace: testEth99,
			fillWindow:   true,
			expected:     10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.fillWindow {
				for _, d := range tt.data {
					if len(d.Details) == 0 {
						continue
					}
					d.window = *utils.NewWindow(WindowSize)
					for i := 0; i < WindowSize; i++ {
						offset := d.Details[i%len(d.Details)].Offset
						d.window.Insert(float64(offset))
					}
				}
			}
			bc := newTestTBCClock(nil)
			bc.data = tt.data
			bc.syncState.leadingIFace = tt.leadingIFace
			result := bc.getLargestOffset()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetLargestOffset_EmptyPTP4lWindowSkipped(t *testing.T) {
	recentTime := time.Now().UnixMilli()

	dpllData := &Data{
		ProcessName: DPLL,
		Details:     []*DataDetails{{IFace: testEth0, Offset: 20, time: recentTime}},
		window:      *utils.NewWindow(WindowSize),
	}
	for i := 0; i < WindowSize; i++ {
		dpllData.window.Insert(20)
	}

	ptp4lData := &Data{
		ProcessName: PTP4l,
		Details:     []*DataDetails{{IFace: testEth0, Offset: 9798319463, time: recentTime}},
		window:      *utils.NewWindow(WindowSize),
	}

	bc := newTestTBCClock(nil)
	bc.data = []*Data{dpllData, ptp4lData}
	bc.syncState.leadingIFace = testEth99

	result := bc.getLargestOffset()
	assert.Equal(t, int64(20), result, "should use DPLL data, skipping PTP4l with empty window")
}

func TestGetLargestOffset_PartiallyFilledWindowBlocksResult(t *testing.T) {
	recentTime := time.Now().UnixMilli()

	dpllData := &Data{
		ProcessName: DPLL,
		Details:     []*DataDetails{{IFace: testEth0, Offset: 20, time: recentTime}},
		window:      *utils.NewWindow(WindowSize),
	}
	dpllData.window.Insert(20)

	bc := newTestTBCClock(nil)
	bc.data = []*Data{dpllData}
	bc.syncState.leadingIFace = testEth99

	result := bc.getLargestOffset()
	assert.Equal(t, FaultyPhaseOffset, result, "partially filled window should return FaultyPhaseOffset")
}

func TestAddEvent_SourceLostPropagation(t *testing.T) {
	now := time.Now().UnixMilli()

	tests := []struct {
		name            string
		initialDetails  []*DataDetails
		event           Event
		expectedStates  map[string]PTPState
		expectedSrcLost map[string]bool
	}{
		{
			name: "source-lost does not propagate to other iface (no cross-hardware bleeding)",
			initialDetails: []*DataDetails{
				{IFace: "eno8903", State: PTP_LOCKED, sourceLost: false, time: now - 5000},
				{IFace: testEno8703, State: PTP_LOCKED, sourceLost: false, time: now - 3000},
			},
			event: Event{
				Source: PTP4l,
				IFace:  testEno8703,
				Time:   now,
				Data:   &PTPData{State: PTP_FREERUN, SourceLost: true},
			},
			expectedStates: map[string]PTPState{
				"eno8903":   PTP_LOCKED,
				testEno8703: PTP_FREERUN,
			},
			expectedSrcLost: map[string]bool{
				"eno8903":   false,
				testEno8703: true,
			},
		},
		{
			name: "source-lost does not overwrite already-FREERUN detail",
			initialDetails: []*DataDetails{
				{IFace: "eno8903", State: PTP_FREERUN, sourceLost: true, time: now - 2000},
				{IFace: testEno8703, State: PTP_LOCKED, sourceLost: false, time: now - 1000},
			},
			event: Event{
				Source: PTP4l,
				IFace:  testEno8703,
				Time:   now,
				Data:   &PTPData{State: PTP_FREERUN, SourceLost: true},
			},
			expectedStates: map[string]PTPState{
				"eno8903":   PTP_FREERUN,
				testEno8703: PTP_FREERUN,
			},
			expectedSrcLost: map[string]bool{
				"eno8903":   true,
				testEno8703: true,
			},
		},
		{
			name: "non-source-lost event does not propagate",
			initialDetails: []*DataDetails{
				{IFace: "eno8903", State: PTP_LOCKED, sourceLost: false, time: now - 5000},
				{IFace: testEno8703, State: PTP_FREERUN, sourceLost: false, time: now - 3000},
			},
			event: Event{
				Source: PTP4l,
				IFace:  testEno8703,
				Time:   now,
				Data:   &PTPData{State: PTP_LOCKED, SourceLost: false},
			},
			expectedStates: map[string]PTPState{
				"eno8903":   PTP_LOCKED,
				testEno8703: PTP_LOCKED,
			},
			expectedSrcLost: map[string]bool{
				"eno8903":   false,
				testEno8703: false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Data{
				ProcessName: PTP4l,
				Details:     tt.initialDetails,
			}
			d.AddEvent(tt.event)
			for _, dd := range d.Details {
				assert.Equal(t, tt.expectedStates[dd.IFace], dd.State, "state for %s", dd.IFace)
				assert.Equal(t, tt.expectedSrcLost[dd.IFace], dd.sourceLost, "sourceLost for %s", dd.IFace)
			}
		})
	}
}

func TestIsSourceLostBC_StaleDetailFixed(t *testing.T) {
	now := time.Now().UnixMilli()

	t.Run("stale LOCKED detail on other iface keeps isSourceLostBC false (no cross-hardware bleeding)", func(t *testing.T) {
		ptp4lData := &Data{
			ProcessName: PTP4l,
			Details: []*DataDetails{
				{IFace: "eno8903", State: PTP_LOCKED, sourceLost: false, time: now - 5000},
				{IFace: testEno8703, State: PTP_LOCKED, sourceLost: false, time: now - 3000},
			},
		}
		dpllData := &Data{
			ProcessName: DPLL,
			Details: []*DataDetails{
				{IFace: testEno8703, State: PTP_LOCKED, time: now},
			},
		}

		bc := newTestTBCClock(nil)
		bc.data = []*Data{ptp4lData, dpllData}

		assert.False(t, bc.isSourceLostBC(), "before source-lost event, ptpLost should be false")

		ptp4lData.AddEvent(Event{
			Source: PTP4l,
			IFace:  testEno8703,
			Time:   now,
			Data:   &PTPData{State: PTP_FREERUN, SourceLost: true},
		})

		assert.False(t, bc.isSourceLostBC(), "stale LOCKED detail on other iface prevents ptpLost")
	})

	t.Run("single LOCKED detail keeps source as not lost", func(t *testing.T) {
		ptp4lData := &Data{
			ProcessName: PTP4l,
			Details: []*DataDetails{
				{IFace: "eno8903", State: PTP_FREERUN, sourceLost: true, time: now},
				{IFace: testEno8703, State: PTP_LOCKED, sourceLost: false, time: now},
			},
		}
		dpllData := &Data{
			ProcessName: DPLL,
			Details: []*DataDetails{
				{IFace: testEno8703, State: PTP_LOCKED, time: now},
			},
		}
		bc := newTestTBCClock(nil)
		bc.data = []*Data{ptp4lData, dpllData}
		assert.False(t, bc.isSourceLostBC())
	})
}

func fillBCDataWindows(bc *TBCClock, offset int64) {
	for _, d := range bc.data {
		d.window = *utils.NewWindow(WindowSize)
		for i := 0; i < WindowSize; i++ {
			d.window.Insert(float64(offset))
		}
	}
}

const testTBCIface = "ens1f0"

func makeTBCEvent(process EventSource, state PTPState, offset int64, sourceLost bool) Event {
	return Event{
		Source:     process,
		IFace:      testTBCIface,
		CfgName:    testPTP4lCfg,
		ClockType:  BC,
		Time:       time.Now().UnixMilli(),
		WriteToLog: true,
		Data: &PTPData{
			State:      state,
			Values:     map[ValueType]interface{}{OFFSET: offset},
			SourceLost: sourceLost,
		},
	}
}

func tbcLeadingClockParams() *LeadingClockParams {
	return &LeadingClockParams{
		leadingInterface:         testTBCIface,
		inSyncConditionThreshold: 100,
		inSyncConditionTimes:     1,
		toFreeRunThreshold:       1500,
		MaxInSpecOffset:          500,
		upstreamParentDataSet:    &protocol.ParentDataSet{},
		upstreamTimeProperties:   &protocol.TimePropertiesDS{},
		downstreamParentDataSet:  &protocol.ParentDataSet{},
		downstreamTimeProperties: &protocol.TimePropertiesDS{},
	}
}

func newLockedTBCClock(io clockIO) *TBCClock {
	if io == nil {
		io = &noopClockIO{}
	}
	bc := &TBCClock{
		io:               io,
		cfgName:          testTS2PHCCfg,
		leadingClockData: tbcLeadingClockParams(),
		syncState: clockSyncState{
			state:         PTP_FREERUN,
			clockClass:    protocol.ClockClassUninitialized,
			clockAccuracy: fbprotocol.ClockAccuracyUnknown,
		},
		overallSyncState: PTP_FREERUN,
		osClockState:     PTP_NOTSET,
	}
	bc.addEvent(makeTBCEvent(DPLL, PTP_LOCKED, 10, false))
	bc.addEvent(makeTBCEvent(PTP4lProcessName, PTP_LOCKED, 10, false))
	fillBCDataWindows(bc, 10)
	bc.addEvent(makeTBCEvent(DPLL, PTP_LOCKED, 10, false))
	return bc
}

func TestUpdateGMState(t *testing.T) {
	const cfg = testTS2PHCCfg
	const iface = "ens1f0"

	makeEvent := func(process EventSource, state PTPState, sourceLost bool) Event {
		e := Event{
			Source:     process,
			IFace:      iface,
			CfgName:    cfg,
			ClockType:  GM,
			Time:       time.Now().UnixMilli(),
			WriteToLog: true,
		}
		if process == GNSS {
			var gpsStatus int64
			if state == PTP_LOCKED {
				gpsStatus = 3
			}
			e.Data = &GNSSData{GPSStatus: gpsStatus, Offset: 0, SourceLost: sourceLost}
		} else {
			e.Data = &PTPData{
				State:      state,
				Values:     map[ValueType]interface{}{OFFSET: int64(0)},
				SourceLost: sourceLost,
			}
		}
		return e
	}

	makeDPLLEvent := func(state PTPState, sourceLost, outOfSpec, frequencyTraceable bool) Event {
		e := makeEvent(DPLL, state, sourceLost)
		d := e.Data.(*PTPData)
		d.OutOfSpec = outOfSpec
		d.FrequencyTraceable = frequencyTraceable
		return e
	}

	type step struct {
		events         []Event
		wantState      PTPState
		wantClockClass fbprotocol.ClockClass
	}

	tests := []struct {
		desc  string
		steps []step
	}{
		{
			desc: "all sources locked",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeEvent(DPLL, PTP_LOCKED, false),
						makeEvent(TS2PHCProcessName, PTP_LOCKED, false),
					},
					wantState:      PTP_LOCKED,
					wantClockClass: fbprotocol.ClockClass6,
				},
			},
		},
		{
			desc: "DPLL locked, GNSS locked, ts2phc freerun",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeEvent(DPLL, PTP_LOCKED, false),
						makeEvent(TS2PHCProcessName, PTP_FREERUN, false),
					},
					wantState:      PTP_FREERUN,
					wantClockClass: protocol.ClockClassFreerun,
				},
			},
		},
		{
			desc: "DPLL holdover",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeEvent(DPLL, PTP_HOLDOVER, false),
						makeEvent(TS2PHCProcessName, PTP_LOCKED, false),
					},
					wantState:      PTP_HOLDOVER,
					wantClockClass: fbprotocol.ClockClass7,
				},
			},
		},
		{
			desc: "DPLL freerun",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeEvent(DPLL, PTP_FREERUN, false),
						makeEvent(TS2PHCProcessName, PTP_LOCKED, false),
					},
					wantState:      PTP_FREERUN,
					wantClockClass: protocol.ClockClassFreerun,
				},
			},
		},
		{
			desc: "DPLL freerun after holdover out of spec",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeDPLLEvent(PTP_FREERUN, false, true, true),
					},
					wantState:      PTP_FREERUN,
					wantClockClass: protocol.ClockClassOutOfSpec,
				},
			},
		},
		{
			desc: "no DPLL yet, GNSS and ts2phc locked",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeEvent(TS2PHCProcessName, PTP_LOCKED, false),
					},
					wantState:      PTP_LOCKED,
					wantClockClass: fbprotocol.ClockClass6,
				},
			},
		},
		{
			desc: "GNSS sourceLost with ts2phc locked - stay with last state",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeEvent(DPLL, PTP_LOCKED, false),
						makeEvent(TS2PHCProcessName, PTP_LOCKED, false),
					},
					wantState:      PTP_LOCKED,
					wantClockClass: fbprotocol.ClockClass6,
				},
				{
					events: []Event{
						makeEvent(GNSS, PTP_FREERUN, true),
					},
					wantState:      PTP_LOCKED,
					wantClockClass: fbprotocol.ClockClass6,
				},
			},
		},
		{
			desc: "DPLL locked, GNSS locked, ts2phc holdover",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeEvent(DPLL, PTP_LOCKED, false),
						makeEvent(TS2PHCProcessName, PTP_HOLDOVER, false),
					},
					wantState:      PTP_HOLDOVER,
					wantClockClass: fbprotocol.ClockClass7,
				},
			},
		},
		{
			desc: "GNSS lost with ts2phc holdover",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_FREERUN, true),
						makeEvent(DPLL, PTP_LOCKED, false),
						makeEvent(TS2PHCProcessName, PTP_HOLDOVER, false),
					},
					wantState:      PTP_HOLDOVER,
					wantClockClass: fbprotocol.ClockClass7,
				},
			},
		},
		{
			desc: "GNSS sourceLost waits for DPLL holdover then transitions to clockClass 7",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeEvent(DPLL, PTP_LOCKED, false),
						makeEvent(TS2PHCProcessName, PTP_LOCKED, false),
					},
					wantState:      PTP_LOCKED,
					wantClockClass: fbprotocol.ClockClass6,
				},
				{
					events: []Event{
						makeEvent(GNSS, PTP_FREERUN, true),
					},
					wantState:      PTP_LOCKED,
					wantClockClass: fbprotocol.ClockClass6,
				},
				{
					events: []Event{
						makeEvent(DPLL, PTP_HOLDOVER, false),
					},
					wantState:      PTP_HOLDOVER,
					wantClockClass: fbprotocol.ClockClass7,
				},
			},
		},
		{
			desc: "GNSS sourceLost then recovery restores clockClass 6",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeEvent(DPLL, PTP_LOCKED, false),
						makeEvent(TS2PHCProcessName, PTP_LOCKED, false),
					},
					wantState:      PTP_LOCKED,
					wantClockClass: fbprotocol.ClockClass6,
				},
				{
					events: []Event{
						makeEvent(GNSS, PTP_FREERUN, true),
						makeEvent(DPLL, PTP_HOLDOVER, false),
					},
					wantState:      PTP_HOLDOVER,
					wantClockClass: fbprotocol.ClockClass7,
				},
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeEvent(DPLL, PTP_LOCKED, false),
					},
					wantState:      PTP_LOCKED,
					wantClockClass: fbprotocol.ClockClass6,
				},
			},
		},
		{
			desc: "GNSS FREERUN without sourceLost goes to clockClass 248",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeEvent(DPLL, PTP_LOCKED, false),
						makeEvent(TS2PHCProcessName, PTP_LOCKED, false),
					},
					wantState:      PTP_LOCKED,
					wantClockClass: fbprotocol.ClockClass6,
				},
				{
					events: []Event{
						makeEvent(GNSS, PTP_FREERUN, false),
					},
					wantState:      PTP_FREERUN,
					wantClockClass: protocol.ClockClassFreerun,
				},
			},
		},
		// --- "Wait for DPLL" rows from state table ---
		{
			desc: "GNSS sourceLost DPLL locked ts2phc freerun - wait for DPLL holdover",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeEvent(DPLL, PTP_LOCKED, false),
						makeEvent(TS2PHCProcessName, PTP_LOCKED, false),
					},
					wantState:      PTP_LOCKED,
					wantClockClass: fbprotocol.ClockClass6,
				},
				{
					events: []Event{
						makeEvent(GNSS, PTP_FREERUN, true),
						makeEvent(TS2PHCProcessName, PTP_FREERUN, false),
					},
					wantState:      PTP_FREERUN,
					wantClockClass: fbprotocol.ClockClass6, // clockClass NOT updated in wait path
				},
			},
		},
		// --- GNSS FREERUN (not sourceLost) rows ---
		{
			desc: "GNSS FREERUN (not SL) DPLL locked ts2phc locked → FREERUN 248",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_FREERUN, false),
						makeEvent(DPLL, PTP_LOCKED, false),
						makeEvent(TS2PHCProcessName, PTP_LOCKED, false),
					},
					wantState:      PTP_FREERUN,
					wantClockClass: protocol.ClockClassFreerun,
				},
			},
		},
		{
			desc: "GNSS FREERUN (not SL) DPLL locked ts2phc freerun → FREERUN 248",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_FREERUN, false),
						makeEvent(DPLL, PTP_LOCKED, false),
						makeEvent(TS2PHCProcessName, PTP_FREERUN, false),
					},
					wantState:      PTP_FREERUN,
					wantClockClass: protocol.ClockClassFreerun,
				},
			},
		},
		// --- No DPLL available rows (dpllState stays PTP_NOTSET) ---
		{
			desc: "no DPLL, GNSS locked, ts2phc freerun → FREERUN 248",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeEvent(TS2PHCProcessName, PTP_FREERUN, false),
					},
					wantState:      PTP_FREERUN,
					wantClockClass: protocol.ClockClassFreerun,
				},
			},
		},
		{
			desc: "no DPLL, GNSS locked, ts2phc holdover → HOLDOVER 7",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeEvent(TS2PHCProcessName, PTP_HOLDOVER, false),
					},
					wantState:      PTP_HOLDOVER,
					wantClockClass: fbprotocol.ClockClass7,
				},
			},
		},
		{
			desc: "no DPLL, GNSS freerun (not SL), ts2phc locked → FREERUN 248",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_FREERUN, false),
						makeEvent(TS2PHCProcessName, PTP_LOCKED, false),
					},
					wantState:      PTP_FREERUN,
					wantClockClass: protocol.ClockClassFreerun,
				},
			},
		},
		{
			desc: "no DPLL, GNSS freerun (not SL), ts2phc freerun → FREERUN 248",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_FREERUN, false),
						makeEvent(TS2PHCProcessName, PTP_FREERUN, false),
					},
					wantState:      PTP_FREERUN,
					wantClockClass: protocol.ClockClassFreerun,
				},
			},
		},
		// --- Out-of-spec edge cases: both flags required for class 140 ---
		{
			desc: "DPLL freerun outOfSpec only (no frequencyTrace) → class 248 not 140",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeDPLLEvent(PTP_FREERUN, false, true, false),
					},
					wantState:      PTP_FREERUN,
					wantClockClass: protocol.ClockClassFreerun,
				},
			},
		},
		{
			desc: "DPLL freerun frequencyTrace only (no outOfSpec) → class 248 not 140",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeDPLLEvent(PTP_FREERUN, false, false, true),
					},
					wantState:      PTP_FREERUN,
					wantClockClass: protocol.ClockClassFreerun,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			gm := &GMClock{
				cfgName: cfg,
				io:      &recordingClockIO{},
				syncState: clockSyncState{
					state:         PTP_NOTSET,
					clockClass:    protocol.ClockClassUninitialized,
					clockAccuracy: fbprotocol.ClockAccuracyUnknown,
				},
				overallSyncState: PTP_NOTSET,
			}

			var result clockSyncState
			for i, s := range tt.steps {
				for _, ev := range s.events {
					_, result = gm.addEvent(ev)
				}

				result = gm.updateState()
				assert.Equal(t, s.wantState, result.state, "step %d: state", i)
				assert.Equal(t, s.wantClockClass, result.clockClass, "step %d: clockClass", i)
			}
		})
	}

	t.Run("nil data → leading interface unknown, state reporting delayed", func(t *testing.T) {
		gm := &GMClock{
			cfgName: cfg,
			io:      &recordingClockIO{},
			syncState: clockSyncState{
				state:         PTP_NOTSET,
				clockClass:    protocol.ClockClassUninitialized,
				clockAccuracy: fbprotocol.ClockAccuracyUnknown,
			},
			overallSyncState: PTP_NOTSET,
		}
		result := gm.updateState()
		assert.Equal(t, LEADING_INTERFACE_UNKNOWN, result.leadingIFace)
		assert.Equal(t, PTPState(""), result.state, "no state set when leading interface unknown")
	})

	t.Run("non-leading DPLL fault forces FREERUN", func(t *testing.T) {
		gm := &GMClock{
			cfgName: cfg,
			io:      &recordingClockIO{},
			syncState: clockSyncState{
				state:         PTP_NOTSET,
				clockClass:    protocol.ClockClassUninitialized,
				clockAccuracy: fbprotocol.ClockAccuracyUnknown,
			},
			overallSyncState: PTP_NOTSET,
		}

		// Leading interface locked
		gm.addEvent(makeEvent(GNSS, PTP_LOCKED, false))
		gm.addEvent(makeEvent(DPLL, PTP_LOCKED, false))
		gm.addEvent(makeEvent(TS2PHCProcessName, PTP_LOCKED, false))

		// Add a second DPLL interface that is NOT locked
		secondDPLL := Event{
			Source:     DPLL,
			IFace:      "ens1f1",
			CfgName:    cfg,
			ClockType:  GM,
			Time:       time.Now().UnixMilli(),
			WriteToLog: true,
			Data: &PTPData{
				State:  PTP_FREERUN,
				Values: map[ValueType]interface{}{OFFSET: int64(0)},
			},
		}
		gm.addEvent(secondDPLL)

		result := gm.updateState()
		assert.Equal(t, PTP_FREERUN, result.state, "non-leading DPLL fault should force FREERUN")
		assert.Equal(t, protocol.ClockClassFreerun, result.clockClass)
	})
}

func TestUpdateBCState(t *testing.T) {
	t.Run("FREERUN to LOCKED", func(t *testing.T) {
		bc := newLockedTBCClock(nil)
		assert.Equal(t, PTP_LOCKED, bc.syncState.state, "should be LOCKED")
	})

	t.Run("LOCKED to FREERUN via offset", func(t *testing.T) {
		bc := newLockedTBCClock(nil)

		_, result, _ := bc.addEvent(makeTBCEvent(DPLL, PTP_LOCKED, 2000, false))
		assert.Equal(t, PTP_FREERUN, result.state, "should transition to FREERUN")
		assert.Equal(t, protocol.ClockClassFreerun, result.clockClass, "clockClass should be 248")
	})

	t.Run("LOCKED to HOLDOVER via source lost", func(t *testing.T) {
		bc := newLockedTBCClock(nil)

		_, result, _ := bc.addEvent(makeTBCEvent(PTP4lProcessName, PTP_FREERUN, 10, true))
		assert.Equal(t, PTP_HOLDOVER, result.state, "should transition to HOLDOVER")
		assert.Equal(t, fbprotocol.ClockClass(135), result.clockClass, "clockClass should be 135 (holdover in-spec)")
	})

	t.Run("HOLDOVER to LOCKED", func(t *testing.T) {
		bc := newLockedTBCClock(nil)

		_, result, _ := bc.addEvent(makeTBCEvent(PTP4lProcessName, PTP_FREERUN, 10, true))
		assert.Equal(t, PTP_HOLDOVER, result.state, "setup: should be HOLDOVER")

		bc.leadingClockData.inSyncThresholdCounter = 0
		bc.addEvent(makeTBCEvent(DPLL, PTP_LOCKED, 5, false))
		fillBCDataWindows(bc, 5)
		_, result, _ = bc.addEvent(makeTBCEvent(PTP4lProcessName, PTP_LOCKED, 5, false))
		assert.Equal(t, PTP_LOCKED, result.state, "should transition back to LOCKED")
	})

	t.Run("HOLDOVER to FREERUN via offset", func(t *testing.T) {
		bc := newLockedTBCClock(nil)

		_, result, _ := bc.addEvent(makeTBCEvent(PTP4lProcessName, PTP_FREERUN, 10, true))
		assert.Equal(t, PTP_HOLDOVER, result.state, "setup: should be HOLDOVER")

		_, result, _ = bc.addEvent(makeTBCEvent(DPLL, PTP_HOLDOVER, 2000, false))
		assert.Equal(t, PTP_FREERUN, result.state, "should transition to FREERUN")
		assert.Equal(t, protocol.ClockClassFreerun, result.clockClass, "clockClass should be 248")
	})

	t.Run("HOLDOVER in-spec to out-of-spec", func(t *testing.T) {
		bc := newLockedTBCClock(nil)

		_, result, _ := bc.addEvent(makeTBCEvent(PTP4lProcessName, PTP_FREERUN, 10, true))
		assert.Equal(t, PTP_HOLDOVER, result.state, "setup: should be HOLDOVER")
		assert.Equal(t, fbprotocol.ClockClass(135), result.clockClass, "setup: should be in-spec (135)")

		fillBCDataWindows(bc, 600)
		_, result, _ = bc.addEvent(makeTBCEvent(DPLL, PTP_HOLDOVER, 600, false))
		assert.Equal(t, PTP_HOLDOVER, result.state, "should stay in HOLDOVER")
		assert.Equal(t, fbprotocol.ClockClass(165), result.clockClass, "clockClass should change to 165 (out-of-spec)")
	})
}

func TestAddEvent_StoresSpecFlags(t *testing.T) {
	t.Run("DPLL event stores outOfSpec and frequencyTraceable in DataDetails", func(t *testing.T) {
		d := &Data{ProcessName: DPLL, State: PTP_UNKNOWN, window: *utils.NewWindow(WindowSize)}
		ev := Event{
			Source:    DPLL,
			IFace:     "ens1f0",
			ClockType: GM,
			Time:      time.Now().UnixMilli(),
			Data:      &PTPData{State: PTP_FREERUN, OutOfSpec: true, FrequencyTraceable: true, Values: map[ValueType]interface{}{OFFSET: int64(0)}},
		}
		d.AddEvent(ev)

		dd := d.GetDataDetails("ens1f0")
		require.NotNil(t, dd)
		assert.True(t, dd.outOfSpec)
		assert.True(t, dd.frequencyTraceable)
	})

	t.Run("non-DPLL PTPData stores its own flags independently", func(t *testing.T) {
		dpll := &Data{ProcessName: DPLL, State: PTP_UNKNOWN, window: *utils.NewWindow(WindowSize)}
		dpll.AddEvent(Event{
			Source: DPLL, IFace: "ens1f0", ClockType: GM, Time: time.Now().UnixMilli(),
			Data: &PTPData{State: PTP_FREERUN, OutOfSpec: true, FrequencyTraceable: true, Values: map[ValueType]interface{}{OFFSET: int64(0)}},
		})

		ts := &Data{ProcessName: TS2PHCProcessName, State: PTP_UNKNOWN, window: *utils.NewWindow(WindowSize)}
		ts.AddEvent(Event{
			Source: TS2PHC, IFace: "ens1f0", ClockType: GM, Time: time.Now().UnixMilli(),
			Data: &PTPData{State: PTP_LOCKED, OutOfSpec: false, FrequencyTraceable: false, Values: map[ValueType]interface{}{OFFSET: int64(0)}},
		})

		dpllDD := dpll.GetDataDetails("ens1f0")
		require.NotNil(t, dpllDD)
		assert.True(t, dpllDD.outOfSpec, "DPLL details should retain its flags")
		assert.True(t, dpllDD.frequencyTraceable)

		tsDD := ts.GetDataDetails("ens1f0")
		require.NotNil(t, tsDD)
		assert.False(t, tsDD.outOfSpec, "ts2phc details should have its own flags")
		assert.False(t, tsDD.frequencyTraceable)
	})
}

func TestWorstOfState(t *testing.T) {
	tests := []struct {
		a, b     PTPState
		expected PTPState
	}{
		{PTP_LOCKED, PTP_LOCKED, PTP_LOCKED},
		{PTP_LOCKED, PTP_FREERUN, PTP_FREERUN},
		{PTP_FREERUN, PTP_LOCKED, PTP_FREERUN},
		{PTP_HOLDOVER, PTP_LOCKED, PTP_HOLDOVER},
		{PTP_LOCKED, PTP_HOLDOVER, PTP_HOLDOVER},
		{PTP_FREERUN, PTP_HOLDOVER, PTP_FREERUN},
		{PTP_HOLDOVER, PTP_FREERUN, PTP_FREERUN},
		{PTP_HOLDOVER, PTP_HOLDOVER, PTP_HOLDOVER},
		{PTP_FREERUN, PTP_FREERUN, PTP_FREERUN},
	}
	for _, tt := range tests {
		t.Run(string(tt.a)+"_"+string(tt.b), func(t *testing.T) {
			assert.Equal(t, tt.expected, worstOfState(tt.a, tt.b))
		})
	}
}

type recordingClockIO struct {
	sync.Mutex
	ipcMessages    []ipc.Message
	emittedClasses []fbprotocol.ClockClass
	gmSettings     protocol.GrandmasterSettings
	gmSettingsErr  error
	setGMCalls     []protocol.GrandmasterSettings
	setGMErr       error
}

func (r *recordingClockIO) emitClockClass(cc fbprotocol.ClockClass, _ string) {
	r.emittedClasses = append(r.emittedClasses, cc)
}
func (r *recordingClockIO) sendIPC(msg ipc.Message) {
	r.ipcMessages = append(r.ipcMessages, msg)
}
func (r *recordingClockIO) getGMSettings(_ string) (protocol.GrandmasterSettings, error) {
	return r.gmSettings, r.gmSettingsErr
}
func (r *recordingClockIO) setGMSettings(_ string, g protocol.GrandmasterSettings) error {
	r.setGMCalls = append(r.setGMCalls, g)
	return r.setGMErr
}
func (r *recordingClockIO) setExternalGMPropertiesNP(string, protocol.ExternalGrandmasterProperties) error {
	return nil
}
func (r *recordingClockIO) getParentTimeAndCurrentDS(string) (pmc.ParentTimeCurrentDS, error) {
	return pmc.ParentTimeCurrentDS{}, nil
}
func (r *recordingClockIO) getUtcOffset() int { return 37 }

func TestProcessSyncE(t *testing.T) {
	t.Run("state event emits synce_state IPC", func(t *testing.T) {
		rio := &recordingClockIO{}
		bc := &TBCClock{cfgName: testPTP4lCfg, io: rio}
		ev := Event{
			Source: SYNCE,
			IFace:  testEns7f0,
			Data: &PTPData{
				Values: map[ValueType]interface{}{
					EEC_STATE: "EEC_LOCKED",
				},
			},
		}
		bc.processSyncE(ev)
		assert.Len(t, rio.ipcMessages, 1)
		assert.Equal(t, ipc.TypeSyncEState, rio.ipcMessages[0].Type)
		assert.Equal(t, testPTP4lCfg, rio.ipcMessages[0].Profile)
		assert.Equal(t, testEns7f0, rio.ipcMessages[0].IFace)
		assert.Equal(t, ipc.SyncEStateValue{State: "EEC_LOCKED"}, rio.ipcMessages[0].Values)
	})

	t.Run("quality event emits synce_clock_quality IPC", func(t *testing.T) {
		rio := &recordingClockIO{}
		bc := &TBCClock{cfgName: testPTP4lCfg, io: rio}
		ev := Event{
			Source: SYNCE,
			IFace:  testEns7f0,
			Data: &PTPData{
				Values: map[ValueType]interface{}{
					QL:     byte(4),
					EXT_QL: byte(0xFF),
				},
			},
		}
		bc.processSyncE(ev)
		assert.Len(t, rio.ipcMessages, 1)
		assert.Equal(t, ipc.TypeSyncEClockQuality, rio.ipcMessages[0].Type)
		assert.Equal(t, testPTP4lCfg, rio.ipcMessages[0].Profile)
		assert.Equal(t, ipc.SyncEClockQualityValue{QL: 4, ExtendedQL: 0xFF}, rio.ipcMessages[0].Values)
	})

	t.Run("event with both state and quality emits two IPC messages", func(t *testing.T) {
		rio := &recordingClockIO{}
		bc := &TBCClock{cfgName: testPTP4lCfg, io: rio}
		ev := Event{
			Source: SYNCE,
			IFace:  testEns7f0,
			Data: &PTPData{
				Values: map[ValueType]interface{}{
					EEC_STATE: "EEC_LOCKED",
					QL:        byte(4),
					EXT_QL:    byte(21),
				},
			},
		}
		bc.processSyncE(ev)
		assert.Len(t, rio.ipcMessages, 2)
		types := []string{rio.ipcMessages[0].Type, rio.ipcMessages[1].Type}
		assert.Contains(t, types, ipc.TypeSyncEState)
		assert.Contains(t, types, ipc.TypeSyncEClockQuality)
	})

	t.Run("nil PTPData does not panic", func(t *testing.T) {
		rio := &recordingClockIO{}
		bc := &TBCClock{cfgName: testPTP4lCfg, io: rio}
		bc.processSyncE(Event{Source: SYNCE, Data: nil})
		assert.Empty(t, rio.ipcMessages)
	})

	t.Run("ts2phc cfgName normalizes to ptp4l profile", func(t *testing.T) {
		rio := &recordingClockIO{}
		bc := &TBCClock{cfgName: testTS2PHCCfg, io: rio}
		ev := Event{
			Source: SYNCE,
			IFace:  testEns7f0,
			Data: &PTPData{
				Values: map[ValueType]interface{}{
					EEC_STATE: "EEC_HOLDOVER",
				},
			},
		}
		bc.processSyncE(ev)
		assert.Len(t, rio.ipcMessages, 1)
		assert.Equal(t, testPTP4lCfg, rio.ipcMessages[0].Profile)
	})
}

func TestUpdateOSClockState(t *testing.T) {
	t.Run("changes when PTP and OS clock differ", func(t *testing.T) {
		rio := &recordingClockIO{}
		bc := newTestTBCClock(nil)
		bc.io = rio
		bc.syncState.state = PTP_LOCKED
		bc.overallSyncState = PTP_LOCKED

		bc.SystemClockUpdate(PTP_FREERUN)
		assert.Equal(t, PTP_FREERUN, bc.overallSyncState)
		assert.Equal(t, PTP_FREERUN, bc.osClockState)
		require.Len(t, rio.ipcMessages, 1)
		assert.Equal(t, ipc.TypeSyncState, rio.ipcMessages[0].Type)
		assert.Equal(t, ipc.SyncStateValue{State: ipc.StateFreerun}, rio.ipcMessages[0].Values)
	})

	t.Run("no change when already correct", func(t *testing.T) {
		rio := &recordingClockIO{}
		bc := newTestTBCClock(nil)
		bc.io = rio
		bc.syncState.state = PTP_LOCKED
		bc.overallSyncState = PTP_LOCKED

		bc.SystemClockUpdate(PTP_LOCKED)
		assert.Equal(t, PTP_LOCKED, bc.overallSyncState)
		assert.Empty(t, rio.ipcMessages)
	})
}

func TestTBCClock_ParentDSUpdate(t *testing.T) {
	t.Run("stores upstream parent dataset", func(t *testing.T) {
		bc := newTestTBCClock(nil)
		parentDS := protocol.ParentDataSet{
			GrandmasterIdentity:   "001122.fffe.334455",
			GrandmasterClockClass: 6,
		}

		bc.ParentDSUpdate(parentDS)

		assert.True(t, parentDS.Equal(bc.leadingClockData.upstreamParentDataSet))
	})

	t.Run("unchanged dataset is a no-op", func(t *testing.T) {
		bc := newTestTBCClock(nil)
		parentDS := protocol.ParentDataSet{
			GrandmasterIdentity:   "001122.fffe.334455",
			GrandmasterClockClass: 6,
		}

		bc.ParentDSUpdate(parentDS)
		bc.ParentDSUpdate(parentDS)

		assert.True(t, parentDS.Equal(bc.leadingClockData.upstreamParentDataSet))
	})

	t.Run("LOCKED clock propagates upstream clock class immediately", func(t *testing.T) {
		rio := &recordingClockIO{}
		bc := newLockedTBCClock(rio)
		assert.Equal(t, PTP_LOCKED, bc.syncState.state, "precondition: clock must be LOCKED")
		rio.ipcMessages = nil

		bc.ParentDSUpdate(protocol.ParentDataSet{
			GrandmasterClockClass: 6,
		})

		assert.Equal(t, fbprotocol.ClockClass(6), bc.syncState.clockClass,
			"clock class should update to upstream value")
		assert.True(t, bc.leadingClockData.upstreamParentDataSet.Equal(
			bc.leadingClockData.downstreamParentDataSet),
			"downstream ParentDataSet should match upstream after propagation")
	})

	t.Run("LOCKED clock sends IPC on clock class change", func(t *testing.T) {
		rio := &recordingClockIO{}
		bc := newLockedTBCClock(rio)
		rio.ipcMessages = nil

		bc.ParentDSUpdate(protocol.ParentDataSet{
			GrandmasterClockClass: 7,
		})

		var foundClockClassIPC bool
		for _, msg := range rio.ipcMessages {
			if msg.Type == ipc.TypeClockClass {
				foundClockClassIPC = true
				assert.Equal(t, ipc.ClockClassValue{ClockClass: 7}, msg.Values)
			}
		}
		assert.True(t, foundClockClassIPC, "should send clock class IPC message")
	})

	t.Run("LOCKED clock unchanged upstream class produces no IPC", func(t *testing.T) {
		rio := &recordingClockIO{}
		bc := newLockedTBCClock(rio)

		bc.ParentDSUpdate(protocol.ParentDataSet{GrandmasterClockClass: 6})
		rio.ipcMessages = nil

		bc.ParentDSUpdate(protocol.ParentDataSet{GrandmasterClockClass: 6})

		var clockClassIPCs int
		for _, msg := range rio.ipcMessages {
			if msg.Type == ipc.TypeClockClass {
				clockClassIPCs++
			}
		}
		assert.Equal(t, 0, clockClassIPCs, "no clock class IPC when upstream class unchanged")
	})
}

func TestBCClock_ParentDSUpdate(t *testing.T) {
	t.Run("updates clock class and emits", func(t *testing.T) {
		rio := &recordingClockIO{}
		bc := &BCClock{cfgName: testPTP4lCfg, io: rio}

		parentDS := protocol.ParentDataSet{
			GrandmasterClockClass: 6,
		}
		bc.ParentDSUpdate(parentDS)

		assert.Equal(t, fbprotocol.ClockClass(6), bc.clockClass)
		require.Len(t, rio.ipcMessages, 1)
		assert.Equal(t, ipc.TypeClockClass, rio.ipcMessages[0].Type)
		assert.Equal(t, ipc.ClockClassValue{ClockClass: 6}, rio.ipcMessages[0].Values)
		require.Len(t, rio.emittedClasses, 1)
		assert.Equal(t, fbprotocol.ClockClass(6), rio.emittedClasses[0])
	})

	t.Run("unchanged class still emits but does not send IPC", func(t *testing.T) {
		rio := &recordingClockIO{}
		bc := &BCClock{cfgName: testPTP4lCfg, io: rio, clockClass: fbprotocol.ClockClass(6)}

		parentDS := protocol.ParentDataSet{
			GrandmasterClockClass: 6,
		}
		bc.ParentDSUpdate(parentDS)

		assert.Empty(t, rio.ipcMessages, "updateClockClass should no-op on unchanged class")
		require.Len(t, rio.emittedClasses, 1, "emitClockClass is always called")
	})
}
