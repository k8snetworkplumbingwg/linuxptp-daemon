package daemon

import (
	"os/exec"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
)

// chronydProcess wraps ptpProcess with logical enable/disable support
// via chronyc online/offline. Only this type satisfies process.Enabler.
type chronydProcess struct {
	*ptpProcess
	enabled bool
}

func (c *chronydProcess) Enable() error {
	out, err := exec.Command("chronyc", "-h", ChronydSocketPath, "online").Output()
	if err != nil {
		glog.Errorf("chronyc online failed: %v, output: %s", err, string(out))
		return err
	}
	c.enabled = true
	processStatus(c.name, c.messageTag, PtpProcessUp)
	sendProcessStatusEvent(c.eventCh, event.EventSource(c.name), c.configName, c.clockType, "", PtpProcessUp)
	return nil
}

func (c *chronydProcess) Disable() error {
	out, err := exec.Command("chronyc", "-h", ChronydSocketPath, "offline").Output()
	if err != nil {
		glog.Errorf("chronyc offline failed: %v, output: %s", err, string(out))
		return err
	}
	c.enabled = false
	processStatus(c.name, c.messageTag, PtpProcessDown)
	sendProcessStatusEvent(c.eventCh, event.EventSource(c.name), c.configName, c.clockType, "", PtpProcessDown)
	return nil
}

func (c *chronydProcess) IsEnabled() bool { return c.enabled }

func (c *chronydProcess) Conditions() map[process.Action]process.Condition {
	return c.conditions
}
