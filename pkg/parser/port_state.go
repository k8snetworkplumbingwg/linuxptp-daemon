package parser

import "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"

// PortStateToRole maps a linuxptp ps_str port-state name (as carried in a
// PORT_DATA_SET management message's portState field) to the internal
// PTPPortRole and ClockState enums.
//
// The mapping mirrors determineRole() in ptp4l_parser.go but operates on a
// single-state string ("SLAVE") rather than a log-line transition phrase
// ("UNCALIBRATED to SLAVE on ..."). Because PORT_DATA_SET only reports the
// current state (not the previous one), the caller is responsible for
// maintaining previous-role context externally.
func PortStateToRole(state string) (constants.PTPPortRole, constants.ClockState) {
	switch state {
	case "SLAVE":
		return constants.PortRoleSlave, constants.ClockStateFreeRun
	case "MASTER", "GRAND_MASTER", "PRE_MASTER":
		return constants.PortRoleMaster, constants.ClockStateFreeRun
	case "PASSIVE":
		return constants.PortRolePassive, constants.ClockStateFreeRun
	case "LISTENING":
		return constants.PortRoleListening, constants.ClockStateFreeRun
	case "FAULTY":
		return constants.PortRoleFaulty, constants.ClockStateHoldover
	default:
		return constants.PortRoleUnknown, constants.ClockStateFreeRun
	}
}
