package daemon

import (
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

// NewTestPtpProcess returns a ptpProcess with a caller-supplied cmd.
func NewTestPtpProcess(cmd ProcessCmd, eventCh chan event.Event) *ptpProcess {
	dn := &Daemon{
		processManager: &ProcessManager{eventsIn: eventCh},
	}
	p := newPtpProcess(event.Ptp4lProcessName, "ptp4l.0.config", "", "",
		"[ptp4l.0.config:{level}]", &ptpv1.PtpProfile{},
		event.ClockUnset, dependenciesFor(dn))
	p.cmd = cmd
	return p
}
