// Package intel describes a set of plugins related to intel PTP hardware
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
		name                 string
		hwplugins            []string
		defaultInitScript    string
		preferredClock       string
		skipGlobalClockChain bool
		cachedClockID        uint64
	}
)

// GetPhaseInputs accessor function
func (o UserData) GetPhaseInputs() []PhaseInputs { return o.PhaseInputs }

// getClockID honors the selected preferred clock module if giver, falling back to PCI Clock ID if not found or not preferred
func (d *PluginData) getClockID(device string) uint64 {
	if d.cachedClockID != 0 {
		return d.cachedClockID
	}
	if d.preferredClock != "" {
		clkID, err := getClockIDByModule(d.preferredClock)
		if err == nil {
			d.cachedClockID = clkID
			return d.cachedClockID
		}
		glog.Errorf("%s: failed to resolve ice DPLL clock ID for %s via netlink: %v", d.name, d.preferredClock, err)
		// Fallback to PCI id if this fetch failed
	}
	return getPCIClockID(device)
}

// TODO: Should this global be moved into PluginData?
var clockChain ClockChainInterface = &ClockChain{}

// userDataFromProfile extracts and casts the appropriate plugin data from the PtpProfile.Plugins structure.
// Returns error for unmarshalling issues, or nil for missing data.
func userDataFromProfile(target string, profile *ptpv1.PtpProfile) (UserData, error) {
	var userData UserData
	if rawJSON, ok := profile.Plugins[target]; ok {
		optsByteArray, _ := json.Marshal(rawJSON)
		err := json.Unmarshal(optsByteArray, &userData)
		if err != nil {
			return userData, fmt.Errorf("%s failed to unmarshal opts: %w", target, err)
		}
	}
	return userData, nil
}

// OnPTPConfigChangeIntel is called after the PTP config has changed
func OnPTPConfigChangeIntel(data *interface{}, nodeProfile *ptpv1.PtpProfile) error {
	pluginData := (*data).(*PluginData)
	target := pluginData.name
	glog.Info("%s: calling onPTPConfigChange", target)

	userData, err := userDataFromProfile(target, nodeProfile)
	if err != nil {
		glog.Error(err)
		return nil
	}

	// for unit testing only, PtpSettings may include "unitTest" key. The value is
	// the path where resulting configuration files will be written, instead of /var/run
	_, unitTest = (*nodeProfile).PtpSettings["unitTest"]
	if unitTest {
		MockPins()
	}

	if userData.EnableDefaultConfig && pluginData.defaultInitScript != "" {
		stdout, _ := exec.Command("/usr/bin/bash", "-c", pluginData.defaultInitScript).Output()
		glog.Infof(string(stdout))
	}
	if (*nodeProfile).PtpSettings == nil {
		(*nodeProfile).PtpSettings = make(map[string]string)
	}
	for device := range userData.DevicePins {
		dpllClockIDStr := fmt.Sprintf("%s[%s]", dpll.ClockIdStr, device)
		(*nodeProfile).PtpSettings[dpllClockIDStr] = strconv.FormatUint(pluginData.getClockID(device), 10)
	}
	if !unitTest {
		applyDevicePins(userData.DevicePins)
	}

	for k, v := range userData.DpllSettings {
		if _, ok := (*nodeProfile).PtpSettings[k]; !ok {
			(*nodeProfile).PtpSettings[k] = strconv.FormatUint(v, 10)
		}
	}
	for iface, properties := range userData.PhaseOffsetPins {
		ifaceFound := false
		for dev := range userData.DevicePins {
			if strings.Compare(iface, dev) == 0 {
				ifaceFound = true
				break
			}
		}
		if !ifaceFound {
			glog.Errorf("%s: phase offset pin filter initialization failed: interface %s not found among  %v",
				target, iface, reflect.ValueOf(userData.DevicePins).MapKeys())
			break
		}
		for pinProperty, value := range properties {
			key := strings.Join([]string{iface, "phaseOffsetFilter", strconv.FormatUint(getPCIClockID(iface), 10), pinProperty}, ".")
			(*nodeProfile).PtpSettings[key] = value
		}
	}
	if userData.PhaseInputs != nil {
		chain, ierr := InitClockChain(userData, nodeProfile)
		if ierr != nil {
			return ierr
		}
		// TODO:: The original e830 plugin implementation set these PtpSettings from its own clockChdain copy, but explicitly did NOT the global clockChain; This may be incorrec
		(*nodeProfile).PtpSettings["leadingInterface"] = chain.GetLeadingNIC().Name
		(*nodeProfile).PtpSettings["upstreamPort"] = chain.GetLeadingNIC().UpstreamPort
		if !pluginData.skipGlobalClockChain {
			clockChain = chain
		}
	} else {
		glog.Error("no clock chain set")
	}

	return nil
}

// AfterRunPTPCommandIntel is invoked by the plugin architecture after various PTP events
func AfterRunPTPCommandIntel(data *interface{}, nodeProfile *ptpv1.PtpProfile, command string) error {
	pluginData := (*data).(*PluginData)
	target := pluginData.name
	glog.Info("%s: Calling AfterRunPTPCommandIntel for command %s", target, command)

	userData, err := userDataFromProfile(target, nodeProfile)
	if err != nil {
		glog.Error(err)
		return nil
	}

	switch command {
	case "gpspipe":
		glog.Infof("%s: Applying ublx config", target)
		// Execute user-supplied UblxCmds first:
		pluginData.hwplugins = append(pluginData.hwplugins, userData.UblxCmds.runAll()...)
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
		OnPTPConfigChange:  OnPTPConfigChangeIntel,
		PopulateHwConfig:   PopulateHwConfigIntel,
		AfterRunPTPCommand: AfterRunPTPCommandIntel,
	}
	return &_plugin, &pluginData
}
