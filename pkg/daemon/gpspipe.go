package daemon

import "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"

type GpsPipe = process.GpsPipe

const (
	GpsPipeSerialPort = process.GpsPipeSerialPort
	GPSD_DIR          = process.GPSD_DIR
)

var NewGpsPipeProcess = process.NewGpsPipeProcess
