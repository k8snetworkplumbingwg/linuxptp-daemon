package parser

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/stretchr/testify/assert"
)

func TestPortStateToRole(t *testing.T) {
	tests := []struct {
		state     string
		wantRole  constants.PTPPortRole
		wantClock constants.ClockState
	}{
		{"SLAVE", constants.PortRoleSlave, constants.ClockStateFreeRun},
		{"MASTER", constants.PortRoleMaster, constants.ClockStateFreeRun},
		{"GRAND_MASTER", constants.PortRoleMaster, constants.ClockStateFreeRun},
		{"PRE_MASTER", constants.PortRoleMaster, constants.ClockStateFreeRun},
		{"PASSIVE", constants.PortRolePassive, constants.ClockStateFreeRun},
		{"LISTENING", constants.PortRoleListening, constants.ClockStateFreeRun},
		{"FAULTY", constants.PortRoleFaulty, constants.ClockStateHoldover},
		{"UNCALIBRATED", constants.PortRoleUnknown, constants.ClockStateFreeRun},
		{"INITIALIZING", constants.PortRoleUnknown, constants.ClockStateFreeRun},
		{"DISABLED", constants.PortRoleUnknown, constants.ClockStateFreeRun},
		{"NONE", constants.PortRoleUnknown, constants.ClockStateFreeRun},
		{"bogus", constants.PortRoleUnknown, constants.ClockStateFreeRun},
	}

	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			role, clock := PortStateToRole(tt.state)
			assert.Equal(t, tt.wantRole, role, "role for %s", tt.state)
			assert.Equal(t, tt.wantClock, clock, "clock state for %s", tt.state)
		})
	}
}
