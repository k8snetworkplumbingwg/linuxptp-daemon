package intel

import (
	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
)

const (
	pluginNameE825 = "e825"
)

// E825 initializes the e825 plugin
func E825(name string) (*plugin.Plugin, *interface{}) {
	if name != pluginNameE825 {
		glog.Errorf("Plugin must be initialized as '%s'", pluginNameE825)
		return nil, nil
	}
	_plugin, _data := NewIntelPlugin(pluginNameE825)
	_data.preferredClock = "zl3073x"
	var iface interface{} = _data
	return _plugin, &iface
}
