package process

import (
	"context"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPMCStartGates(t *testing.T) {
	p := &PMCProcess{}
	p.processState = Starting
	require.NoError(t, p.Start(context.Background()))
	assert.Equal(t, Starting, p.State())

	p.processState = Running
	require.NoError(t, p.Start(context.Background()))
	assert.Equal(t, Running, p.State())

	p.processState = Stopping
	err := p.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is stopping")
}

func TestPMCStopCreatedIsStopped(t *testing.T) {
	p := &PMCProcess{}
	require.NoError(t, p.Stop())
	assert.True(t, p.Stopped())
	assert.Equal(t, Created, p.State())
}

func TestPMCProcess_Profile(t *testing.T) {
	prof := &ptpv1.PtpProfile{}
	p := NewPMCProcess(0, nil, event.OC, prof, -1)
	assert.Equal(t, prof, p.Profile())
}
