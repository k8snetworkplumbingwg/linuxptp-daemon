package clock

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/stretchr/testify/require"
)

func TestClockEventResultDefaultReporting(t *testing.T) {
	gm, _, _ := newTestGMClock(t)
	tbc := newPMCTestTBCClock(nil)
	bc, err := NewBC("bc.config", false, testThreshold)
	require.NoError(t, err)
	oc, err := NewBC("oc.config", true, testThreshold)
	require.NoError(t, err)
	ha, _, _ := newTestHAClock()
	for _, receiver := range []Clock{gm, tbc, bc, oc, ha} {
		t.Run(string(receiver.ClockType()), func(t *testing.T) {
			result := receiver.AddEvent(event.Event{Source: event.PMC, CfgName: receiver.ConfigName()})
			require.Nil(t, result.Report)
			require.False(t, result.SuppressReport)
			require.Equal(t, receiver.GetState(), result.State)
		})
	}
	member := ha.members[testPTP4lCfg]
	result := ha.AddEvent(event.Event{Source: event.PMC, CfgName: testPTP4lCfg})
	require.Equal(t, member.GetState(), result.State)
	require.Equal(t, member.ClockClass(), result.ClockClass)
	require.Equal(t, ha.haProfileStatus(), result.HAProfileStatus)
	require.Nil(t, result.Report)
	require.False(t, result.SuppressReport)
}
