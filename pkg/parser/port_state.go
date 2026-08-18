package parser

import "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"

// PortStateToRole maps a linuxptp ps_str port-state name (as carried in a
// PORT_DATA_SET management message's portState field) to the internal
// PTPPortRole enum. Because PORT_DATA_SET only reports the current state
// (not the previous one), the caller is responsible for maintaining
// previous-role context externally.
func PortStateToRole(state string) constants.PTPPortRole {
	switch state {
	case "SLAVE":
		return constants.PortRoleSlave
	case "MASTER", "GRAND_MASTER", "PRE_MASTER":
		return constants.PortRoleMaster
	case "PASSIVE":
		return constants.PortRolePassive
	case "LISTENING":
		return constants.PortRoleListening
	case "FAULTY":
		return constants.PortRoleFaulty
	default:
		return constants.PortRoleUnknown
	}
}
