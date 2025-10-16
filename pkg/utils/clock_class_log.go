package utils

import (
	"fmt"
	"net"
	"time"

	"github.com/golang/glog"
)

func GetClockClassLogMessage(name, configName string, clockClass uint8) string {
	return fmt.Sprintf(
		"%s[%d]:[%s] CLOCK_CLASS_CHANGE %d\n",
		name, time.Now().Unix(), configName, clockClass,
	)
}

func EmitClockClass(c net.Conn, name string, configName string, clockClass uint8) {
	if c == nil {
		glog.Error("failed to write class change event: nil socket provided")
		return
	}

	_, err := c.Write([]byte(GetClockClassLogMessage(name, configName, clockClass)))
	if err != nil {
		glog.Errorf("failed to write class change event: %s", err.Error())
	}
}
