// # Assisted by watsonx Code Assistant
package event

import (
	"sync"
	"testing"
	"time"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/stretchr/testify/assert"
)

type MockData struct {
	Data map[string][]*Data
}

type noopClockIO struct{ sync.Mutex }

func (n *noopClockIO) announceClockClass(fbprotocol.ClockClass, fbprotocol.ClockAccuracy, string) {
}
func (n *noopClockIO) emitClockClass(fbprotocol.ClockClass, string) {}
func (n *noopClockIO) getStoredClockClass(string) (fbprotocol.ClockClass, bool) {
	return 0, false
}
func (n *noopClockIO) updateDownstreamData(*BCClock, string) {}
func (n *noopClockIO) sendIPC(ipc.Message)                    {}

func newTestBCClock(lcp *LeadingClockParams) *BCClock {
	if lcp == nil {
		lcp = newLeadingClockParams()
	}
	return &BCClock{
		io:               &noopClockIO{},
		leadingClockData: lcp,
		syncState: clockSyncState{
			state:         PTP_FREERUN,
			clockClass:    protocol.ClockClassUninitialized,
			clockAccuracy: fbprotocol.ClockAccuracyUnknown,
		},
		overallSyncState: PTP_FREERUN,
	}
}

func TestAddEvent_NormalizesCfgName(t *testing.T) {
	t.Run("PTP4l event gets cfgName from BCClock", func(t *testing.T) {
		bc := newTestBCClock(nil)
		bc.cfgName = "ts2phc.0.config"
		ev := Event{
			Source:  PTP4lProcessName,
			CfgName: "ptp4l.0.config",
			IFace:   testEth0,
		}
		ev, _, _ = bc.addEvent(ev)
		assert.Equal(t, "ts2phc.0.config", ev.CfgName)
	})

	t.Run("Non-PTP4l event keeps original cfgName", func(t *testing.T) {
		bc := newTestBCClock(nil)
		bc.cfgName = "ts2phc.0.config"
		ev := Event{
			Source:  DPLL,
			CfgName: "ts2phc.0.config",
			IFace:   testEth0,
		}
		ev, _, _ = bc.addEvent(ev)
		assert.Equal(t, "ts2phc.0.config", ev.CfgName)
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

	bc := newTestBCClock(&LeadingClockParams{})
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

	bc := newTestBCClock(&LeadingClockParams{})
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
			bc := newTestBCClock(tt.lcp)
			result := bc.getLeadingInterfaceBC()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestInSpecCondition(t *testing.T) {
	t.Run("returns false when MaxInSpecOffset is 0", func(t *testing.T) {
		bc := newTestBCClock(&LeadingClockParams{MaxInSpecOffset: 0})
		result := bc.inSpecCondition()
		assert.False(t, result)
	})

	t.Run("returns false when offset is out of spec", func(t *testing.T) {
		bc := newTestBCClock(&LeadingClockParams{MaxInSpecOffset: 5})
		bc.data = []*Data{
			{ProcessName: DPLL, Details: []*DataDetails{{IFace: testIface1Lower, Offset: 10}}},
		}
		bc.syncState.leadingIFace = testIface1Lower
		result := bc.inSpecCondition()
		assert.False(t, result)
	})

	t.Run("returns true when offset is in spec", func(t *testing.T) {
		bc := newTestBCClock(&LeadingClockParams{MaxInSpecOffset: 5})
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
			bc := newTestBCClock(&LeadingClockParams{toFreeRunThreshold: tt.threshold})
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
			bc := newTestBCClock(nil)
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

	bc := newTestBCClock(nil)
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

	bc := newTestBCClock(nil)
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
			name: "source-lost propagates to stale LOCKED detail on different iface",
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
				"eno8903":   PTP_FREERUN,
				testEno8703: PTP_FREERUN,
			},
			expectedSrcLost: map[string]bool{
				"eno8903":   true,
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

	t.Run("stale LOCKED detail no longer fools isSourceLostBC after source-lost propagation", func(t *testing.T) {
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

		bc := newTestBCClock(nil)
		bc.data = []*Data{ptp4lData, dpllData}

		assert.False(t, bc.isSourceLostBC(), "before source-lost event, ptpLost should be false")

		ptp4lData.AddEvent(Event{
			Source: PTP4l,
			IFace:  testEno8703,
			Time:   now,
			Data:   &PTPData{State: PTP_FREERUN, SourceLost: true},
		})

		assert.True(t, bc.isSourceLostBC(), "after source-lost propagation, ptpLost should be true")
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
		bc := newTestBCClock(nil)
		bc.data = []*Data{ptp4lData, dpllData}
		assert.False(t, bc.isSourceLostBC())
	})
}

func fillBCDataWindows(bc *BCClock, offset int64) {
	for _, d := range bc.data {
		d.window = *utils.NewWindow(WindowSize)
		for i := 0; i < WindowSize; i++ {
			d.window.Insert(float64(offset))
		}
	}
}

func TestUpdateGMState(t *testing.T) {
	const cfg = "ts2phc.0.config"
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

	type step struct {
		events         []Event
		outOfSpec      bool
		frequencyTrace bool
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
						makeEvent(DPLL, PTP_FREERUN, false),
					},
					outOfSpec:      true,
					frequencyTrace: true,
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
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			e := &EventHandler{
				data:         map[string][]*Data{},
				clkSyncState: map[string]*clockSyncState{},
			}

			for i, s := range tt.steps {
				for _, ev := range s.events {
					e.addEvent(ev)
				}
				e.outOfSpec = s.outOfSpec
				e.frequencyTraceable = s.frequencyTrace

				result := e.updateGMState(cfg)
				assert.Equal(t, s.wantState, result.state, "step %d: state", i)
				assert.Equal(t, s.wantClockClass, result.clockClass, "step %d: clockClass", i)
			}
		})
	}
}

func TestUpdateBCState(t *testing.T) {
	const cfg = "ptp4l.0.config"
	const iface = "ens1f0"

	makeBCEvent := func(process EventSource, state PTPState, offset int64, sourceLost bool) Event {
		return Event{
			Source:     process,
			IFace:      iface,
			CfgName:    cfg,
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

	bcLCP := func() *LeadingClockParams {
		return &LeadingClockParams{
			leadingInterface:         iface,
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

	newBCSetup := func() *BCClock {
		bc := newTestBCClock(bcLCP())
		return bc
	}

	t.Run("FREERUN to LOCKED", func(t *testing.T) {
		bc := newBCSetup()

		bc.addEvent(makeBCEvent(DPLL, PTP_LOCKED, 10, false))
		bc.addEvent(makeBCEvent(PTP4lProcessName, PTP_LOCKED, 10, false))
		fillBCDataWindows(bc, 10)

		_, result, _ := bc.addEvent(makeBCEvent(DPLL, PTP_LOCKED, 10, false))
		assert.Equal(t, PTP_LOCKED, result.ClockState.state, "should transition to LOCKED")
	})

	t.Run("LOCKED to FREERUN via offset", func(t *testing.T) {
		bc := newBCSetup()

		bc.addEvent(makeBCEvent(DPLL, PTP_LOCKED, 10, false))
		bc.addEvent(makeBCEvent(PTP4lProcessName, PTP_LOCKED, 10, false))
		fillBCDataWindows(bc, 10)
		_, result, _ := bc.addEvent(makeBCEvent(DPLL, PTP_LOCKED, 10, false))
		assert.Equal(t, PTP_LOCKED, result.ClockState.state, "setup: should be LOCKED")

		_, result, _ = bc.addEvent(makeBCEvent(DPLL, PTP_LOCKED, 2000, false))
		assert.Equal(t, PTP_FREERUN, result.ClockState.state, "should transition to FREERUN")
		assert.Equal(t, protocol.ClockClassFreerun, result.ClockState.clockClass, "clockClass should be 248")
	})

	t.Run("LOCKED to HOLDOVER via source lost", func(t *testing.T) {
		bc := newBCSetup()

		bc.addEvent(makeBCEvent(DPLL, PTP_LOCKED, 10, false))
		bc.addEvent(makeBCEvent(PTP4lProcessName, PTP_LOCKED, 10, false))
		fillBCDataWindows(bc, 10)
		_, result, _ := bc.addEvent(makeBCEvent(DPLL, PTP_LOCKED, 10, false))
		assert.Equal(t, PTP_LOCKED, result.ClockState.state, "setup: should be LOCKED")

		_, result, _ = bc.addEvent(makeBCEvent(PTP4lProcessName, PTP_FREERUN, 10, true))
		assert.Equal(t, PTP_HOLDOVER, result.ClockState.state, "should transition to HOLDOVER")
		assert.Equal(t, fbprotocol.ClockClass(135), result.ClockState.clockClass, "clockClass should be 135 (holdover in-spec)")
	})

	t.Run("HOLDOVER to LOCKED", func(t *testing.T) {
		bc := newBCSetup()

		bc.addEvent(makeBCEvent(DPLL, PTP_LOCKED, 10, false))
		bc.addEvent(makeBCEvent(PTP4lProcessName, PTP_LOCKED, 10, false))
		fillBCDataWindows(bc, 10)
		bc.addEvent(makeBCEvent(DPLL, PTP_LOCKED, 10, false))

		_, result, _ := bc.addEvent(makeBCEvent(PTP4lProcessName, PTP_FREERUN, 10, true))
		assert.Equal(t, PTP_HOLDOVER, result.ClockState.state, "setup: should be HOLDOVER")

		bc.leadingClockData.inSyncThresholdCounter = 0
		bc.addEvent(makeBCEvent(DPLL, PTP_LOCKED, 5, false))
		fillBCDataWindows(bc, 5)
		_, result, _ = bc.addEvent(makeBCEvent(PTP4lProcessName, PTP_LOCKED, 5, false))
		assert.Equal(t, PTP_LOCKED, result.ClockState.state, "should transition back to LOCKED")
	})

	t.Run("HOLDOVER to FREERUN via offset", func(t *testing.T) {
		bc := newBCSetup()

		bc.addEvent(makeBCEvent(DPLL, PTP_LOCKED, 10, false))
		bc.addEvent(makeBCEvent(PTP4lProcessName, PTP_LOCKED, 10, false))
		fillBCDataWindows(bc, 10)
		bc.addEvent(makeBCEvent(DPLL, PTP_LOCKED, 10, false))

		_, result, _ := bc.addEvent(makeBCEvent(PTP4lProcessName, PTP_FREERUN, 10, true))
		assert.Equal(t, PTP_HOLDOVER, result.ClockState.state, "setup: should be HOLDOVER")

		_, result, _ = bc.addEvent(makeBCEvent(DPLL, PTP_HOLDOVER, 2000, false))
		assert.Equal(t, PTP_FREERUN, result.ClockState.state, "should transition to FREERUN")
		assert.Equal(t, protocol.ClockClassFreerun, result.ClockState.clockClass, "clockClass should be 248")
	})

	t.Run("HOLDOVER in-spec to out-of-spec", func(t *testing.T) {
		bc := newBCSetup()

		bc.addEvent(makeBCEvent(DPLL, PTP_LOCKED, 10, false))
		bc.addEvent(makeBCEvent(PTP4lProcessName, PTP_LOCKED, 10, false))
		fillBCDataWindows(bc, 10)
		bc.addEvent(makeBCEvent(DPLL, PTP_LOCKED, 10, false))

		_, result, _ := bc.addEvent(makeBCEvent(PTP4lProcessName, PTP_FREERUN, 10, true))
		assert.Equal(t, PTP_HOLDOVER, result.ClockState.state, "setup: should be HOLDOVER")
		assert.Equal(t, fbprotocol.ClockClass(135), result.ClockState.clockClass, "setup: should be in-spec (135)")

		fillBCDataWindows(bc, 600)
		_, result, _ = bc.addEvent(makeBCEvent(DPLL, PTP_HOLDOVER, 600, false))
		assert.Equal(t, PTP_HOLDOVER, result.ClockState.state, "should stay in HOLDOVER")
		assert.Equal(t, fbprotocol.ClockClass(165), result.ClockState.clockClass, "clockClass should change to 165 (out-of-spec)")
	})
}

func TestUpdateSpecState(t *testing.T) {
	newHandler := func() *EventHandler {
		return &EventHandler{
			data:         map[string][]*Data{},
			clkSyncState: map[string]*clockSyncState{},
		}
	}

	t.Run("DPLL event sets outOfSpec", func(t *testing.T) {
		e := newHandler()
		assert.False(t, e.outOfSpec, "initial outOfSpec should be false")

		e.updateSpecState(Event{Source: DPLL, Data: &PTPData{OutOfSpec: true}})
		assert.True(t, e.outOfSpec, "DPLL event with OutOfSpec=true should set outOfSpec")

		e.updateSpecState(Event{Source: DPLL, Data: &PTPData{OutOfSpec: false}})
		assert.False(t, e.outOfSpec, "DPLL event with OutOfSpec=false should clear outOfSpec")
	})

	t.Run("DPLL event sets frequencyTraceable", func(t *testing.T) {
		e := newHandler()
		assert.False(t, e.frequencyTraceable, "initial frequencyTraceable should be false")

		e.updateSpecState(Event{Source: DPLL, Data: &PTPData{FrequencyTraceable: true}})
		assert.True(t, e.frequencyTraceable, "DPLL event should set frequencyTraceable")

		e.updateSpecState(Event{Source: DPLL, Data: &PTPData{FrequencyTraceable: false}})
		assert.False(t, e.frequencyTraceable, "DPLL event should clear frequencyTraceable")
	})

	t.Run("non-DPLL event does not change spec state", func(t *testing.T) {
		e := newHandler()

		e.updateSpecState(Event{Source: DPLL, Data: &PTPData{OutOfSpec: true, FrequencyTraceable: true}})
		assert.True(t, e.outOfSpec)
		assert.True(t, e.frequencyTraceable)

		e.updateSpecState(Event{Source: TS2PHC, Data: &PTPData{OutOfSpec: false, FrequencyTraceable: false}})
		assert.True(t, e.outOfSpec, "non-DPLL event should not change outOfSpec")
		assert.True(t, e.frequencyTraceable, "non-DPLL event should not change frequencyTraceable")
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

func TestUpdateOverallSyncState(t *testing.T) {
	t.Run("changes when PTP and OS clock differ", func(t *testing.T) {
		bc := newTestBCClock(nil)
		bc.syncState.state = PTP_LOCKED
		bc.overallSyncState = PTP_LOCKED

		changed := bc.updateOverallSyncState(PTP_FREERUN)
		assert.True(t, changed)
		assert.Equal(t, PTP_FREERUN, bc.overallSyncState)
	})

	t.Run("no change when already correct", func(t *testing.T) {
		bc := newTestBCClock(nil)
		bc.syncState.state = PTP_LOCKED
		bc.overallSyncState = PTP_LOCKED

		changed := bc.updateOverallSyncState(PTP_LOCKED)
		assert.False(t, changed)
		assert.Equal(t, PTP_LOCKED, bc.overallSyncState)
	})
}
