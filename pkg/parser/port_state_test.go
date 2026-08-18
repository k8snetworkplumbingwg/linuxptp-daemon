package parser

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/stretchr/testify/assert"
)

func TestPortStateToRole(t *testing.T) {
	tests := []struct {
		state    string
		wantRole constants.PTPPortRole
	}{
		{"SLAVE", constants.PortRoleSlave},
		{"MASTER", constants.PortRoleMaster},
		{"GRAND_MASTER", constants.PortRoleMaster},
		{"PRE_MASTER", constants.PortRoleMaster},
		{"PASSIVE", constants.PortRolePassive},
		{"LISTENING", constants.PortRoleListening},
		{"FAULTY", constants.PortRoleFaulty},
		{"UNCALIBRATED", constants.PortRoleUnknown},
		{"INITIALIZING", constants.PortRoleUnknown},
		{"DISABLED", constants.PortRoleUnknown},
		{"NONE", constants.PortRoleUnknown},
		{"bogus", constants.PortRoleUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			assert.Equal(t, tt.wantRole, PortStateToRole(tt.state))
		})
	}
}
