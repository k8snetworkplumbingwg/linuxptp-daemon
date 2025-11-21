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

const (
	pluginNameE825 = "e825"
)

// EnableE825PTPConfig is the script to enable default e825 PTP configuration
var EnableE825PTPConfig = `
#!/bin/bash
set -eu

echo "No E825 specific configuration is needed"
`

// OnPTPConfigChangeE825 performs actions on PTP config change for e825 plugin
func OnPTPConfigChangeE825(_ *interface{}, nodeProfile *ptpv1.PtpProfile) error {
	glog.Info("calling onPTPConfigChange for e825 plugin")
	var e825Opts UserData
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

// E825 initializes the e825 plugin
func E825(name string) (*plugin.Plugin, *interface{}) {
	if name != pluginNameE825 {
		glog.Errorf("Plugin must be initialized as '%s'", pluginNameE825)
		return nil, nil
	}
	_plugin, _data := NewIntelPlugin(pluginNameE825)
	_plugin.OnPTPConfigChange = OnPTPConfigChangeE825
	var iface interface{} = _data
	return _plugin, &iface
}
