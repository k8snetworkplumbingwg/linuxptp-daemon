package clockmgr

import (
	"testing"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	parserconstants "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testClockmgrETH0        = "eth0"
	testClockmgrPtp4lConfig = "ptp4l.0.config"
)

// GetPTPState returns the stored PTP state for source under cfgName.
func (m *ClockManager) GetPTPState(source event.EventSource, cfgName string) event.PTPState {
	clk := m.GetClock(cfgName)
	if clk == nil {
		return event.PTP_UNKNOWN
	}
	d := clk.GetData(source)
	if d == nil {
		return event.PTP_UNKNOWN
	}
	return d.State
}

// GetPortRole returns the last port role event stored for cfgName/portName.
func (m *ClockManager) GetPortRole(cfgName, portName string) *parser.PTPEvent {
	m.clockManagementMu.Lock()
	defer m.clockManagementMu.Unlock()
	if m.portRole == nil {
		return nil
	}
	ports, ok := m.portRole[cfgName]
	if !ok {
		return nil
	}
	return ports[portName]
}

// IPCCache returns the IPC cache, or nil if not configured.
func (m *ClockManager) IPCCache() *ipc.Cache {
	return m.ipcCache
}

func TestClockManager_GetDataAndPTPState(t *testing.T) {
	cm := Init("test-node", make(chan event.Event), nil, nil, nil, nil)
	_, err := cm.AddClock("ts2phc.0.config", event.OC, nil, "")
	require.NoError(t, err)

	d := cm.GetClock("ts2phc.0.config").GetData(event.TS2PHC)
	require.NotNil(t, d)
	assert.Equal(t, event.PTP_UNKNOWN, cm.GetPTPState(event.TS2PHC, "ts2phc.0.config"))

	ev := event.Event{
		Source:  event.TS2PHC,
		CfgName: "ts2phc.0.config",
		IFace:   testClockmgrETH0,
		Data: &event.OffsetData{
			State:  event.PTP_LOCKED,
			Offset: 100,
		},
	}
	d.AddEvent(ev)
	d.UpdateState()
	assert.Equal(t, event.PTP_LOCKED, cm.GetPTPState(event.TS2PHC, "ts2phc.0.config"))
	assert.Nil(t, cm.GetClock("missing.config"))
	assert.Equal(t, event.PTP_UNKNOWN, cm.GetPTPState(event.TS2PHC, "missing.config"))
}

func TestClockManager_SetPortRole(t *testing.T) {
	cm := Init("test-node", make(chan event.Event), nil, nil, nil, nil)
	ev := &parser.PTPEvent{Role: parserconstants.PortRoleSlave}
	cm.SetPortRole(testClockmgrPtp4lConfig, "ens3f0", ev)
	assert.Equal(t, ev, cm.GetPortRole(testClockmgrPtp4lConfig, "ens3f0"))
	assert.Nil(t, cm.GetPortRole(testClockmgrPtp4lConfig, "ens3f1"))
}

func TestClockManager_EmitClockClassAndIPCCache(t *testing.T) {
	cache := ipc.NewCache(10)
	cm := Init("test-node", make(chan event.Event), nil, nil, nil, cache)
	assert.Equal(t, cache, cm.IPCCache())

	clk, err := cm.AddClock(testClockmgrPtp4lConfig, event.OC, nil, "")
	require.NoError(t, err)
	bc, ok := clk.(*clock.BCClock)
	require.True(t, ok)

	bc.AddEvent(event.Event{
		Source: event.PMC,
		Data:   &event.ParentDSData{ParentDataSet: protocol.ParentDataSet{GrandmasterClockClass: 6}},
	})
	require.Equal(t, fbprotocol.ClockClass(6), bc.ClockClass())

	found := false
	for _, msg := range cache.Snapshot() {
		if msg.Type == ipc.TypeClockClass {
			found = true
			assert.Equal(t, uint8(6), msg.Values.(ipc.ClockClassValue).ClockClass)
		}
	}
	assert.True(t, found, "clock class IPC should be cached after ParentDS update")
}

func TestClockManager_GetWindowsPrefersMatchingProcess(t *testing.T) {
	cm := Init("test-node", make(chan event.Event), nil, nil, nil, nil)
	_, err := cm.AddClock(testClockmgrPtp4lConfig, event.OC, nil, "")
	require.NoError(t, err)

	ptp4lEv := event.Event{
		Source:  event.PTP4l,
		CfgName: testClockmgrPtp4lConfig,
		IFace:   testClockmgrETH0,
		Data:    &event.OffsetData{State: event.PTP_LOCKED, Offset: 10},
	}
	ts2phcEv := event.Event{
		Source:  event.TS2PHC,
		CfgName: "ts2phc.0.config",
		IFace:   testClockmgrETH0,
		Data:    &event.OffsetData{State: event.PTP_LOCKED, Offset: 99},
	}
	// AddEvent only records the offset window after the iface already exists.
	cm.GetClock(testClockmgrPtp4lConfig).GetData(event.PTP4l).AddEvent(ptp4lEv)
	cm.GetClock(testClockmgrPtp4lConfig).GetData(event.PTP4l).AddEvent(ptp4lEv)
	cm.GetClock("ts2phc.0.config").GetData(event.TS2PHC).AddEvent(ts2phcEv)
	cm.GetClock("ts2phc.0.config").GetData(event.TS2PHC).AddEvent(ts2phcEv)

	windows := cm.GetWindows()
	require.NotNil(t, windows[testClockmgrPtp4lConfig])
	require.NotNil(t, windows["ts2phc.0.config"])
	assert.Equal(t, float64(10), windows[testClockmgrPtp4lConfig].LastInserted())
	assert.Equal(t, float64(99), windows["ts2phc.0.config"].LastInserted())
	assert.Nil(t, windows["missing.config"])
}

func TestClockManager_GetWindowsOmitsMismatchedProcess(t *testing.T) {
	cm := Init("test-node", make(chan event.Event), nil, nil, nil, nil)
	_, err := cm.AddClock(testClockmgrPtp4lConfig, event.OC, nil, "")
	require.NoError(t, err)

	ts2phcEv := event.Event{
		Source:  event.TS2PHC,
		CfgName: "ts2phc.0.config",
		IFace:   testClockmgrETH0,
		Data:    &event.OffsetData{State: event.PTP_LOCKED, Offset: 99},
	}
	cm.GetClock("ts2phc.0.config").GetData(event.TS2PHC).AddEvent(ts2phcEv)

	windows := cm.GetWindows()
	require.NotNil(t, windows["ts2phc.0.config"])
	assert.Nil(t, windows[testClockmgrPtp4lConfig], "ptp4l alias must not reuse the ts2phc offset window")
}

func TestClockManager_SetTBCLeadingInterface(t *testing.T) {
	cm := Init("test-node", make(chan event.Event), nil, nil, nil, nil)
	clk, err := cm.AddClock("ptp4l.1.config", event.TBC, &pmc.MockClient{}, "ens1f0")
	require.NoError(t, err)

	state := clk.AddEvent(event.Event{
		Source:  event.PTP4l,
		CfgName: "ptp4l.1.config",
		IFace:   "ens1f1",
		Data:    &event.OffsetData{State: event.PTP_FREERUN, Offset: 49880},
	})
	assert.Equal(t, "ens1f0", state.LeadingIFace)
}
