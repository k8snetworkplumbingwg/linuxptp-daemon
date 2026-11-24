package process

import (
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

// NewTestGpsPipeProcess returns a GpsPipe from NewGpsPipeProcess with a
// caller-supplied cmd and serial-port path (empty serialPort keeps the default).
func NewTestGpsPipeProcess(messageTag string, eventCh chan event.Event, cmd ProcessCmd, serialPort string, gpsdConfig string) *GpsPipe {
	gp := NewGpsPipeProcess(messageTag, &ptpv1.PtpProfile{}, eventCh, gpsdConfig).(*GpsPipe)
	gp.cmd = cmd
	if serialPort != "" {
		gp.serialPort = serialPort
	}
	return gp
}

// NewTestGpsdProcess returns a GPSD from NewGpsdProcess with a caller-supplied cmd.
func NewTestGpsdProcess(serialPort, gmInterface, messageTag string, eventCh chan event.Event, cmd ProcessCmd) *GPSD {
	g := NewGpsdProcess(serialPort, gmInterface, messageTag, nil, nil, &ptpv1.PtpProfile{}, eventCh, config.ProcessConfig{EventChannel: eventCh}).(*GPSD)
	g.cmd = cmd
	return g
}
