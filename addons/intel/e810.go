package intel

import (
	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
)

const (
	pluginNameE810 = "e810"

	// Sourced from https://github.com/RHsyseng/oot-ice/blob/main/ptp-config.sh
	enableE810PTPConfig = `
#!/bin/bash
set -eu

ETH=$(grep -e 000e -e 000f /sys/class/net/*/device/subsystem_device | awk -F"/" '{print $5}')

for DEV in $ETH; do
  if [ -f /sys/class/net/$DEV/device/ptp/ptp*/pins/U.FL2 ]; then
    echo 0 2 > /sys/class/net/$DEV/device/ptp/ptp*/pins/U.FL2
    echo 0 1 > /sys/class/net/$DEV/device/ptp/ptp*/pins/U.FL1
    echo 0 2 > /sys/class/net/$DEV/device/ptp/ptp*/pins/SMA2
    echo 0 1 > /sys/class/net/$DEV/device/ptp/ptp*/pins/SMA1
  fi
done

echo "Disabled all SMA and U.FL Connections"
`
)

// E810 initializes the e810 plugin
func E810(name string) (*plugin.Plugin, *interface{}) {
	if name != pluginNameE810 {
		glog.Errorf("Plugin must be initialized as '%s'", pluginNameE810)
		return nil, nil
	}
	_plugin, data := NewIntelPlugin(pluginNameE810)
	data.defaultInitScript = enableE810PTPConfig
	var iface interface{} = data
	return _plugin, &iface
}
