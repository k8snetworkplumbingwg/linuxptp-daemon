package process

import (
	"context"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestGpsdStartStopRestart(t *testing.T) {
	ch := make(chan event.Event, 8)
	g := NewTestGpsdProcess("", "", "", ch, NewMockCmd())
	g.setState(Dead)

	require.NoError(t, g.Start(context.Background()))
	waitProcessState(t, g, Running)
	waitProcessStatus(t, ch, PtpProcessUp)

	firstStopCh := g.stopCh
	require.NoError(t, g.Stop())
	waitProcessState(t, g, Stopped)
	waitProcessStatus(t, ch, PtpProcessDown)

	require.NoError(t, g.Start(context.Background()))
	waitProcessState(t, g, Running)
	assert.NotEqual(t, firstStopCh, g.stopCh)
	select {
	case <-g.stopCh:
		t.Fatal("stopCh should be open after Start")
	default:
	}
	require.NoError(t, g.Stop())
	waitProcessState(t, g, Stopped)
}

func TestGpsdStartGates(t *testing.T) {
	g := NewTestGpsdProcess("", "", "", nil, nil)
	g.setState(Starting)
	require.NoError(t, g.Start(context.Background()))
	assert.Equal(t, Starting, g.State())

	g.setState(Running)
	require.NoError(t, g.Start(context.Background()))
	assert.Equal(t, Running, g.State())

	g.setState(Stopping)
	err := g.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is stopping")
}

func TestGpsdCrashDead(t *testing.T) {
	ch := make(chan event.Event, 8)
	g := NewTestGpsdProcess("", "", "", ch, NewDeadMockCmd())
	g.setState(Dead)
	require.NoError(t, g.Start(context.Background()))
	waitProcessState(t, g, Dead)
	waitProcessStatus(t, ch, PtpProcessDown)
}

func TestGpspipeNilCmdNoPanic(t *testing.T) {
	ch := make(chan event.Event, 8)
	gp := NewTestGpsPipeProcess("", ch, nil, "", "")
	require.NotPanics(t, func() {
		require.NoError(t, gp.Start(context.Background()))
	})
	waitProcessState(t, gp, Dead)
	waitProcessStatus(t, ch, PtpProcessDown)
}

func TestGpspipeStartGates(t *testing.T) {
	gp := NewTestGpsPipeProcess("", nil, nil, "", "")
	gp.setState(Starting)
	require.NoError(t, gp.Start(context.Background()))
	assert.Equal(t, Starting, gp.State())

	gp.setState(Running)
	require.NoError(t, gp.Start(context.Background()))
	assert.Equal(t, Running, gp.State())

	gp.setState(Stopping)
	err := gp.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is stopping")
}
