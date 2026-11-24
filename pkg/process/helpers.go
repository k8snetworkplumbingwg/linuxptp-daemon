package process

import (
	"fmt"
	"strings"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

const (
	PtpProcessDown int64 = 0
	PtpProcessUp   int64 = 1
)

func AddScheduling(profile *ptpv1.PtpProfile, cmdLine string) string {
	if profile.PtpSchedulingPolicy != nil && *profile.PtpSchedulingPolicy == "SCHED_FIFO" {
		if profile.PtpSchedulingPriority == nil {
			glog.Errorf("Priority must be set for SCHED_FIFO; using default scheduling.")
			return cmdLine
		}
		priority := *profile.PtpSchedulingPriority
		if priority < 1 || priority > 65 {
			glog.Errorf("Invalid priority %d; using default scheduling.", priority)
			return cmdLine
		}
		cmdLine = fmt.Sprintf("/bin/chrt -f %d %s", priority, cmdLine)
		glog.Infof(cmdLine)
	}
	return cmdLine
}

func ConfigNameFromMessageTag(messageTag string) string {
	cfgName := strings.Replace(strings.Replace(messageTag, "]", "", 1), "[", "", 1)
	if cfgName != "" {
		cfgName = strings.Split(cfgName, ":")[0]
	}
	return cfgName
}

func ProcessStatus(processName, messageTag string, status int64) {
	glog.V(14).Infof("processStatus: process=%s config=%s status=%d", processName, ConfigNameFromMessageTag(messageTag), status)
}

func SendProcessStatusEvent(ch chan<- event.Event, source event.EventSource, cfgName string, clockType event.ClockType, iface string, status int64) {
	if ch != nil {
		ch <- event.ProcessStatusEvent(source, cfgName, clockType, iface, status)
	}
}
