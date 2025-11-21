// Package intel describes a set of plugins related to intel PTP hardware
package intel

import (
	"encoding/json"
	"fmt"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

type (
	// UserData defines the user configuration data common to all intel plugins
	UserData struct {
		EnableDefaultConfig bool                         `json:"enableDefaultConfig"`
		UblxCmds            UblxCmdList                  `json:"ublxCmds"`
		DevicePins          DevicePinConfig              `json:"pins"`
		DpllSettings        map[string]uint64            `json:"settings"`
		PhaseOffsetPins     map[string]map[string]string `json:"phaseOffsetPins"`
		PhaseInputs         []PhaseInputs                `json:"interconnections"`
	}

	// PluginData contains the device-specific plugin and status data
	PluginData struct {
		name      string
		hwplugins []string
	}
)

// GetPhaseInputs accessor function
func (o UserData) GetPhaseInputs() []PhaseInputs { return o.PhaseInputs }

// AfterRunPTPCommandIntel is invoked by the plugin architecture after various PTP events
func AfterRunPTPCommandIntel(data *interface{}, nodeProfile *ptpv1.PtpProfile, command string) error {
	pluginData := (*data).(*PluginData)
	target := pluginData.name
	glog.Info("%s: Calling AfterRunPTPCommandIntel for command %s", target, command)
	var pluginOpts UserData
	var err error
	var optsByteArray []byte

	pluginOpts.EnableDefaultConfig = false

	for name, opts := range (*nodeProfile).Plugins {
		if name == target {
			optsByteArray, _ = json.Marshal(opts)
			err = json.Unmarshal(optsByteArray, &pluginOpts)
			if err != nil {
				glog.Errorf("%s failed to unmarshal opts: "+err.Error(), target)
			}
			switch command {
			case "gpspipe":
				glog.Infof("%s: Applying ublx config", target)
				// Execute user-supplied UblxCmds first:
				pluginData.hwplugins = append(pluginData.hwplugins, pluginOpts.UblxCmds.runAll()...)
				// Finish with the default commands:
				pluginData.hwplugins = append(pluginData.hwplugins, defaultUblxCmds().runAll()...)
			case "tbc-ho-exit":
				_, err = clockChain.EnterNormalTBC()
				if err != nil {
					return fmt.Errorf("%s: failed to enter T-BC normal mode: %w", target, err)
				}
				glog.Info("%s: enter T-BC normal mode", target)
			case "tbc-ho-entry":
				_, err = clockChain.EnterHoldoverTBC()
				if err != nil {
					return fmt.Errorf("%s: failed to enter T-BC holdover: %w", target, err)
				}
				glog.Info("%s: enter T-BC holdover", target)
			case "reset-to-default":
				_, err = clockChain.SetPinDefaults()
				if err != nil {
					return fmt.Errorf("%s: failed to reset pins to default: %w", target, err)
				}
				glog.Info("%s: reset pins to default", target)
			default:
				glog.Infof("%s: Doing nothing for command %s", target, command)
			}
		}
	}
	return nil
}

// PopulateHwConfigIntel is invoked by the plugin architecture to populate status information
func PopulateHwConfigIntel(data *interface{}, hwconfigs *[]ptpv1.HwConfig) error {
	pluginData := (*data).(*PluginData)
	for _, hwconfig := range pluginData.hwplugins {
		*hwconfigs = append(*hwconfigs, ptpv1.HwConfig{
			DeviceID: pluginData.name,
			Status:   hwconfig,
		})
	}
	return nil
}

// NewIntelPlugin populates a new Intel plugin with some common defaults
func NewIntelPlugin(name string) (*plugin.Plugin, *PluginData) {
	glog.Infof("registering %s plugin", name)
	pluginData := PluginData{
		name: name,
	}
	_plugin := plugin.Plugin{
		Name:               name,
		PopulateHwConfig:   PopulateHwConfigIntel,
		AfterRunPTPCommand: AfterRunPTPCommandIntel,
	}
	return &_plugin, &pluginData
}
