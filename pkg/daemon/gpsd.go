package daemon

import "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"

type GPSD = process.GPSD

var NewGpsdProcess = process.NewGpsdProcess
