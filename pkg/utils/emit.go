package utils

import (
	"fmt"
	"net"
	"time"

	"github.com/golang/glog"
)

func EmitClockClass(c net.Conn, name string, configName string, clockClass uint8) {
	clockClassOut := fmt.Sprintf("%s[%d]:[%s] CLOCK_CLASS_CHANGE %d\n", name, time.Now().Unix(), configName, clockClass)
	_, err := c.Write([]byte(clockClassOut))
	if err != nil {
		glog.Errorf("failed to write class change event %s", err.Error())
	}
}
