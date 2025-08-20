package generic

import (
	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
)

type FailoverPluginData struct {
}

func Failover(name string) (*plugin.Plugin, *interface{}) {
	if name != "failover" {
		glog.Errorf("Plugin must be initialized as 'failover'")
		return nil, nil
	}
	_plugin := plugin.Plugin{Name: "failover"}
	pluginData := FailoverPluginData{}
	var iface interface{} = &pluginData
	return &_plugin, &iface
}
