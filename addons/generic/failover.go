package generic

import (
	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

type FailoverPluginData struct {
	cmdStop map[string]func()
	cmdRun map[string]func(bool, *plugin.PluginManager)
	stdoutToSocket bool
	pm *plugin.PluginManager
}

func OnPTPConfigChangeFailover(data *interface{}, nodeProfile *ptpv1.PtpProfile) error {
	if data != nil {
		_data := *data
		var pluginData *FailoverPluginData = _data.(*FailoverPluginData)
		_pluginData := *pluginData
		_pluginData.cmdRun = make(map[string]func(bool, *plugin.PluginManager))
		_pluginData.cmdStop = make(map[string]func())
	}
	return nil
}

func RegisterProcessFailover(data *interface{}, pname string, cmdRun func(bool, *plugin.PluginManager), cmdStop func(), stdoutToSocket bool, pm *plugin.PluginManager) {
	if data != nil {
		_data := *data
		var pluginData *FailoverPluginData = _data.(*FailoverPluginData)
		_pluginData := *pluginData
		
		_pluginData.cmdStop[pname] = cmdStop
		_pluginData.cmdRun[pname] = cmdRun
		_pluginData.stdoutToSocket = stdoutToSocket
		_pluginData.pm = pm
	}
}

func Failover(name string) (*plugin.Plugin, *interface{}) {
	if name != "failover" {
		glog.Errorf("Plugin must be initialized as 'failover'")
		return nil, nil
	}
	_plugin := plugin.Plugin{Name: "failover",
		OnPTPConfigChange: OnPTPConfigChangeFailover,
		RegisterProcess:   RegisterProcessFailover}
	pluginData := FailoverPluginData{}
	var iface interface{} = &pluginData
	return &_plugin, &iface
}
