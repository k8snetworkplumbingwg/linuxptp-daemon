package utils

import (
	"fmt"
	"net"
	"time"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/golang/glog"
)

// GetClockClassLogMessage formats a clock class change message with timestamp.
func GetClockClassLogMessage(name, configName string, clockClass fbprotocol.ClockClass) string {
	return fmt.Sprintf(
		"%s[%d]:[%s] CLOCK_CLASS_CHANGE %d\n",
		name, time.Now().Unix(), configName, clockClass,
	)
}

// EmitClockClass writes a clock class change event to the provided network connection.
func EmitClockClass(c net.Conn, name string, configName string, clockClass fbprotocol.ClockClass) {
	if c == nil {
		glog.Error("failed to write class change event: nil socket provided")
		return
	}

	_, err := c.Write([]byte(GetClockClassLogMessage(name, configName, clockClass)))
	if err != nil {
		glog.Errorf("failed to write class change event: %s", err.Error())
	}
}
