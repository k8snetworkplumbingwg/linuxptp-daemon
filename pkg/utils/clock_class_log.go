package utils

import (
	"fmt"
	"net"
	"time"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/socket"
)

// GetClockClassLogMessage formats a clock class change message with timestamp.
func GetClockClassLogMessage(name, configName string, clockClass fbprotocol.ClockClass) string {
	return fmt.Sprintf(
		"%s[%d]:[%s] CLOCK_CLASS_CHANGE %d\n",
		name, time.Now().Unix(), configName, clockClass,
	)
}

// EmitClockClass writes a clock class change event to log and the network connection if provided.
// Deprecated: Use EmitClockClassWithSocket instead for automatic reconnection on broken pipe.
func EmitClockClass(c net.Conn, name string, configName string, clockClass fbprotocol.ClockClass) {
	glog.Info(GetClockClassLogMessage(name, configName, clockClass))
	if c == nil {
		return
	}

	_, err := c.Write([]byte(GetClockClassLogMessage(name, configName, clockClass)))
	if err != nil {
		glog.Errorf("failed to write class change event to socket: %s", err.Error())
	}
}

// EmitClockClassWithSocket writes a clock class change event to log and the reconnectable socket.
// This version handles broken pipe errors by automatically reconnecting.
func EmitClockClassWithSocket(rs *socket.ReconnectableSocket, name string, configName string, clockClass fbprotocol.ClockClass) {
	msg := GetClockClassLogMessage(name, configName, clockClass)
	glog.Info(msg)
	if rs == nil {
		return
	}

	_, err := rs.Write([]byte(msg))
	if err != nil {
		glog.Errorf("failed to write class change event to socket: %s", err.Error())
	}
}
