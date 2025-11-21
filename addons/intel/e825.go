package intel

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"reflect"
	"strconv"
	"strings"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

var pluginNameE825 = "e825"

// E825Opts is the options structure for e825 plugin
type E825Opts struct {
	EnableDefaultConfig bool                         `json:"enableDefaultConfig"`
	UblxCmds            UblxCmdList                  `json:"ublxCmds"`
	DevicePins          DevicePinConfig              `json:"pins"`
	DpllSettings        map[string]uint64            `json:"settings"`
	PhaseOffsetPins     map[string]map[string]string `json:"phaseOffsetPins"`
	PhaseInputs         []PhaseInputs                `json:"interconnections"`
}

// GetPhaseInputs implements PhaseInputsProvider
func (o E825Opts) GetPhaseInputs() []PhaseInputs { return o.PhaseInputs }

// E825PluginData is the data structure for e825 plugin
type E825PluginData struct {
	hwplugins *[]string
}

// EnableE825PTPConfig is the script to enable default e825 PTP configuration
var EnableE825PTPConfig = `
#!/bin/bash
set -eu

echo "No E825 specific configuration is needed"
`

// OnPTPConfigChangeE825 performs actions on PTP config change for e825 plugin
func OnPTPConfigChangeE825(_ *interface{}, nodeProfile *ptpv1.PtpProfile) error {
	glog.Info("calling onPTPConfigChange for e825 plugin")
	var e825Opts E825Opts
	var err error
	var optsByteArray []byte
	for name, opts := range (*nodeProfile).Plugins {
		if name == pluginNameE825 { // "e825"
			optsByteArray, _ = json.Marshal(opts)
			err = json.Unmarshal(optsByteArray, &e825Opts)
			if err != nil {
				glog.Error("e825 failed to unmarshal opts: " + err.Error())
			}
			// for unit testing only, PtpSettings may include "unitTest" key. The value is
			// the path where resulting configuration files will be written, instead of /var/run
			_, unitTest = (*nodeProfile).PtpSettings["unitTest"]
			if unitTest {
				MockPins()
			}

			if e825Opts.EnableDefaultConfig {
				stdout, _ := exec.Command("/usr/bin/bash", "-c", EnableE825PTPConfig).Output()
				glog.Infof(string(stdout))
			}
			if (*nodeProfile).PtpSettings == nil {
				(*nodeProfile).PtpSettings = make(map[string]string)
			}

			// Prefer ZL3073x module for e825
			zlClockID, zlErr := getClockIDByModule("zl3073x")
			if zlErr != nil {
				glog.Errorf("e825: failed to resolve ZL3073x DPLL clock ID via netlink: %v", zlErr)
			}
			for device := range e825Opts.DevicePins {
				dpllClockIDStr := fmt.Sprintf("%s[%s]", dpll.ClockIdStr, device)
				if !unitTest {
					if zlErr == nil {
						(*nodeProfile).PtpSettings[dpllClockIDStr] = strconv.FormatUint(zlClockID, 10)
					} else {
						(*nodeProfile).PtpSettings[dpllClockIDStr] = strconv.FormatUint(getPCIClockID(device), 10)
					}
				}
			}
			if !unitTest {
				applyDevicePins(e825Opts.DevicePins)
			}

			for k, v := range e825Opts.DpllSettings {
				if _, ok := (*nodeProfile).PtpSettings[k]; !ok {
					(*nodeProfile).PtpSettings[k] = strconv.FormatUint(v, 10)
				}
			}
			for iface, properties := range e825Opts.PhaseOffsetPins {
				ifaceFound := false
				for dev := range e825Opts.DevicePins {
					if strings.Compare(iface, dev) == 0 {
						ifaceFound = true
						break
					}
				}
				if !ifaceFound {
					glog.Errorf("e825 phase offset pin filter initialization failed: interface %s not found among  %v",
						iface, reflect.ValueOf(e825Opts.DevicePins).MapKeys())
					break
				}
				for pinProperty, value := range properties {
					var clockIDUsed uint64
					if zlErr == nil {
						clockIDUsed = zlClockID
					} else {
						clockIDUsed = getPCIClockID(iface)
					}
					key := strings.Join([]string{iface, "phaseOffsetFilter", strconv.FormatUint(clockIDUsed, 10), pinProperty}, ".")
					(*nodeProfile).PtpSettings[key] = value
				}
			}
			if e825Opts.PhaseInputs != nil {
				clockChain, err = InitClockChain(e825Opts, nodeProfile)
				if err != nil {
					return err
				}
				(*nodeProfile).PtpSettings["leadingInterface"] = clockChain.GetLeadingNIC().Name
				(*nodeProfile).PtpSettings["upstreamPort"] = clockChain.GetLeadingNIC().UpstreamPort
			} else {
				glog.Error("no clock chain set")
			}
		}
	}
	return nil
}

// AfterRunPTPCommandE825 performs actions after certain PTP commands for e825 plugin
func AfterRunPTPCommandE825(data *interface{}, nodeProfile *ptpv1.PtpProfile, command string) error {
	pluginData := (*data).(*E825PluginData)
	glog.Info("calling AfterRunPTPCommandE825 for e825 plugin")
	var e825Opts E825Opts
	var err error
	var optsByteArray []byte

	e825Opts.EnableDefaultConfig = false

	for name, opts := range (*nodeProfile).Plugins {
		if name == pluginNameE825 {
			optsByteArray, _ = json.Marshal(opts)
			err = json.Unmarshal(optsByteArray, &e825Opts)
			if err != nil {
				glog.Error("e825 failed to unmarshal opts: " + err.Error())
			}
			switch command {
			case "gpspipe":
				glog.Infof("AfterRunPTPCommandE810 doing ublx config for command: %s", command)
				// Execute user-supplied UblxCmds first:
				*pluginData.hwplugins = append(*pluginData.hwplugins, e825Opts.UblxCmds.runAll()...)
				// Finish with the default commands:
				*pluginData.hwplugins = append(*pluginData.hwplugins, defaultUblxCmds().runAll()...)
			case "tbc-ho-exit":
				_, err = clockChain.EnterNormalTBC()
				if err != nil {
					return fmt.Errorf("e825: failed to enter T-BC normal mode")
				}
				glog.Info("e825: enter T-BC normal mode")
			case "tbc-ho-entry":
				_, err = clockChain.EnterHoldoverTBC()
				if err != nil {
					return fmt.Errorf("e825: failed to enter T-BC holdover")
				}
				glog.Info("e825: enter T-BC holdover")
			case "reset-to-default":
				_, err = clockChain.SetPinDefaults()
				if err != nil {
					return fmt.Errorf("e825: failed to reset pins to default")
				}
				glog.Info("e825: reset pins to default")
			default:
				glog.Infof("AfterRunPTPCommandE825 doing nothing for command: %s", command)
			}
		}
	}
	return nil
}

// PopulateHwConfigE825 populates hwconfig for e825 plugin
func PopulateHwConfigE825(data *interface{}, hwconfigs *[]ptpv1.HwConfig) error {
	//hwConfig := ptpv1.HwConfig{}
	//hwConfig.DeviceID = "e825"
	//*hwconfigs = append(*hwconfigs, hwConfig)
	if data != nil {
		_data := *data
		pluginData := _data.(*E825PluginData)
		_pluginData := *pluginData
		if _pluginData.hwplugins != nil {
			for _, _hwconfig := range *_pluginData.hwplugins {
				hwConfig := ptpv1.HwConfig{}
				hwConfig.DeviceID = "e825"
				hwConfig.Status = _hwconfig
				*hwconfigs = append(*hwconfigs, hwConfig)
			}
		}
	}
	return nil
}

// E825 initializes the e825 plugin
func E825(name string) (*plugin.Plugin, *interface{}) {
	if name != "e825" {
		glog.Errorf("Plugin must be initialized as 'e825'")
		return nil, nil
	}
	glog.Infof("registering e825 plugin")
	hwplugins := []string{}
	pluginData := E825PluginData{hwplugins: &hwplugins}
	_plugin := plugin.Plugin{
		Name:               "e825",
		OnPTPConfigChange:  OnPTPConfigChangeE825,
		AfterRunPTPCommand: AfterRunPTPCommandE825,
		PopulateHwConfig:   PopulateHwConfigE825,
	}
	var iface interface{} = &pluginData
	return &_plugin, &iface
}
