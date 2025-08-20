package plugin

import (
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

type Plugin struct {
	Name               string
	Options            interface{}
	OnPTPConfigChange  OnPTPConfigChange
	AfterRunPTPCommand AfterRunPTPCommand
	PopulateHwConfig   PopulateHwConfig
	RegisterProcess    RegisterProcess
	ProcessLog         ProcessLog
}

type PluginManager struct {
	Plugins map[string]*Plugin
	Data    map[string]*interface{}
}

type New func(string) (*Plugin, *interface{})
type OnPTPConfigChange func(*interface{}, *ptpv1.PtpProfile) error
type PopulateHwConfig func(*interface{}, *[]ptpv1.HwConfig) error
type AfterRunPTPCommand func(*interface{}, *ptpv1.PtpProfile, string) error
type RegisterProcess func(*interface{}, string, func(bool, *PluginManager), func(), bool, *PluginManager)
type ProcessLog func(*interface{}, string, string) string

func (pm *PluginManager) OnPTPConfigChange(nodeProfile *ptpv1.PtpProfile) {
	for pluginName, pluginObject := range pm.Plugins {
		pluginFunc := pluginObject.OnPTPConfigChange
		if pluginFunc != nil {
			pluginFunc(pm.Data[pluginName], nodeProfile)
		}
	}
}

func (pm *PluginManager) AfterRunPTPCommand(nodeProfile *ptpv1.PtpProfile, command string) {
	for pluginName, pluginObject := range pm.Plugins {
		pluginFunc := pluginObject.AfterRunPTPCommand
		if pluginFunc != nil {
			pluginFunc(pm.Data[pluginName], nodeProfile, command)
		}
	}
}

func (pm *PluginManager) PopulateHwConfig(hwconfigs *[]ptpv1.HwConfig) {
	for pluginName, pluginObject := range pm.Plugins {
		pluginFunc := pluginObject.PopulateHwConfig
		if pluginFunc != nil {
			pluginFunc(pm.Data[pluginName], hwconfigs)
		}
	}
}

func (pm *PluginManager) RegisterProcess(pname string, cmdRun func(bool, *PluginManager), cmdStop func(), stdoutToSocket bool) {
	for pluginName, pluginObject := range pm.Plugins {
		pluginFunc := pluginObject.RegisterProcess
		if pluginFunc != nil {
			pluginFunc(pm.Data[pluginName], pname, cmdRun, cmdStop, stdoutToSocket, pm)
		}
	}
}

func (pm *PluginManager) ProcessLog(pname string, log string) string {
	ret := log
	for pluginName, pluginObject := range pm.Plugins {
		pluginFunc := pluginObject.ProcessLog
		if pluginFunc != nil {
			ret = pluginFunc(pm.Data[pluginName], pname, ret)
		}
	}
	return ret
}
