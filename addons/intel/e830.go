package intel

import (
	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
)

const (
	pluginNameE830 = "e830"
)

// E830 initializes the e830 plugin
func E830(name string) (*plugin.Plugin, *interface{}) {
	if name != pluginNameE830 {
		glog.Errorf("Plugin must be initialized as '%s'", pluginNameE830)
		return nil, nil
	}
	_plugin, data := NewIntelPlugin(pluginNameE830)
	data.preferredClock = "ice"
	data.skipGlobalClockChain = true // The global clockChain is owned by e825 not e830
	_plugin.AfterRunPTPCommand = nil // No-op for e830
	_plugin.PopulateHwConfig = nil   // No-op for e830
	var iface interface{} = data
	return _plugin, &iface
}
