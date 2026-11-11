package clockmgr

import (
	"io"
	"os"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/stretchr/testify/require"
)

func TestProcessEventDefaultLogSnapshot(t *testing.T) {
	manager := Init("test-node", make(chan event.Event, 16), nil, nil, nil, nil)
	t.Cleanup(manager.RemoveAllClocks)
	gm, err := clock.NewGM("ts2phc.0.config", func() int { return 37 }, &pmc.MockClient{})
	require.NoError(t, err)
	require.NoError(t, manager.AddClock(gm))
	raw := &event.OffsetData{State: event.PTP_FREERUN, Offset: 42, NMEALocked: utils.ToPtr(true)}
	before := raw.String()
	ev := event.Event{CfgName: "ts2phc.0.config", IFace: "ens2f0", Source: event.TS2PHC, ClockType: event.GM, Time: 1, Data: raw, WriteToLog: true}
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
	require.Contains(t, string(output), before)
	require.False(t, *raw.NMEALocked)
	require.NotEqual(t, before, raw.String())
}
