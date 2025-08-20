package generic

import (
	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

type FailoverPluginData struct {
	cmdStop        map[string]func()
	cmdRun         map[string]func(bool, *plugin.PluginManager)
	stdoutToSocket bool
	pm             *plugin.PluginManager
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
		if _pluginData.cmdRun == nil {
			_pluginData.cmdRun = make(map[string]func(bool, *plugin.PluginManager))
		}
		if _pluginData.cmdStop == nil {
			_pluginData.cmdStop = make(map[string]func())
		}
		_pluginData.cmdStop[pname] = cmdStop
		_pluginData.cmdRun[pname] = cmdRun
		_pluginData.stdoutToSocket = stdoutToSocket
		_pluginData.pm = pm
	}
}

func ProcessLogFailover(data *interface{}, pname string, log string) string {
	ret := log
	if data != nil {
		//print("failover %s %s", pname, log)
	}
	return ret
}

func Failover(name string) (*plugin.Plugin, *interface{}) {
	if name != "failover" {
		glog.Errorf("Plugin must be initialized as 'failover'")
		return nil, nil
	}
	_plugin := plugin.Plugin{Name: "failover",
		OnPTPConfigChange: OnPTPConfigChangeFailover,
		RegisterProcess:   RegisterProcessFailover,
		ProcessLog:        ProcessLogFailover,
	}
	pluginData := FailoverPluginData{}
	pluginData.cmdRun = make(map[string]func(bool, *plugin.PluginManager))
	pluginData.cmdStop = make(map[string]func())
	var iface interface{} = &pluginData
	return &_plugin, &iface
}
