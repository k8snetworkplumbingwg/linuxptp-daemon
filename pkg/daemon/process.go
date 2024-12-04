package daemon

import "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"

type process interface {
	Name() string
	Stopped() bool
	CmdStop()
	CmdInit()
	CmdRun(stdToSocket bool)
	MonitorProcess(p config.ProcessConfig)
	ExitCh() chan struct{}
}
