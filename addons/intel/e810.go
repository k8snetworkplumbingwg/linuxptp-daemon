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
	dpll_netlink "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

const (
	pluginNameE810 = "e810"
)

// Sourced from https://github.com/RHsyseng/oot-ice/blob/main/ptp-config.sh
var EnableE810PTPConfig = `
#!/bin/bash
set -eu

ETH=$(grep -e 000e -e 000f /sys/class/net/*/device/subsystem_device | awk -F"/" '{print $5}')

for DEV in $ETH; do
  if [ -f /sys/class/net/$DEV/device/ptp/ptp*/pins/U.FL2 ]; then
    echo 0 2 > /sys/class/net/$DEV/device/ptp/ptp*/pins/U.FL2
    echo 0 1 > /sys/class/net/$DEV/device/ptp/ptp*/pins/U.FL1
    echo 0 2 > /sys/class/net/$DEV/device/ptp/ptp*/pins/SMA2
    echo 0 1 > /sys/class/net/$DEV/device/ptp/ptp*/pins/SMA1
  fi
done

echo "Disabled all SMA and U.FL Connections"
`

var (
	unitTest   bool
	clockChain ClockChainInterface = &ClockChain{}
)

// For mocking DPLL pin info
var DpllPins = []*dpll_netlink.PinInfo{}

func OnPTPConfigChangeE810(data *interface{}, nodeProfile *ptpv1.PtpProfile) error {
	glog.Info("calling onPTPConfigChange for e810 plugin")
	var e810Opts UserData
	var err error
	var optsByteArray []byte
	var stdout []byte

	e810Opts.EnableDefaultConfig = false

	for name, opts := range (*nodeProfile).Plugins {
		if name == "e810" {
			optsByteArray, _ = json.Marshal(opts)
			err = json.Unmarshal(optsByteArray, &e810Opts)
			if err != nil {
				glog.Error("e810 failed to unmarshal opts: " + err.Error())
			}
			// for unit testing only, PtpSettings may include "unitTest" key. The value is
			// the path where resulting configuration files will be written, instead of /var/run
			_, unitTest = (*nodeProfile).PtpSettings["unitTest"]
			if unitTest {
				MockPins()
			}

			if e810Opts.EnableDefaultConfig {
				stdout, _ = exec.Command("/usr/bin/bash", "-c", EnableE810PTPConfig).Output()
				glog.Infof(string(stdout))
			}
			if (*nodeProfile).PtpSettings == nil {
				(*nodeProfile).PtpSettings = make(map[string]string)
			}
			for device := range e810Opts.DevicePins {
				dpllClockIDStr := fmt.Sprintf("%s[%s]", dpll.ClockIdStr, device)
				(*nodeProfile).PtpSettings[dpllClockIDStr] = strconv.FormatUint(getPCIClockID(device), 10)
			}
			if !unitTest {
				applyDevicePins(e810Opts.DevicePins)
			}

			for k, v := range e810Opts.DpllSettings {
				if _, ok := (*nodeProfile).PtpSettings[k]; !ok {
					(*nodeProfile).PtpSettings[k] = strconv.FormatUint(v, 10)
				}
			}
			for iface, properties := range e810Opts.PhaseOffsetPins {
				ifaceFound := false
				for dev := range e810Opts.DevicePins {
					if strings.Compare(iface, dev) == 0 {
						ifaceFound = true
						break
					}
				}
				if !ifaceFound {
					glog.Errorf("e810 phase offset pin filter initialization failed: interface %s not found among  %v",
						iface, reflect.ValueOf(e810Opts.DevicePins).MapKeys())
					break
				}
				for pinProperty, value := range properties {
					key := strings.Join([]string{iface, "phaseOffsetFilter", strconv.FormatUint(getPCIClockID(iface), 10), pinProperty}, ".")
					(*nodeProfile).PtpSettings[key] = value
				}
			}
			if e810Opts.PhaseInputs != nil {
				clockChain, err = InitClockChain(e810Opts, nodeProfile)
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

// E810 initializes the e810 plugin
func E810(name string) (*plugin.Plugin, *interface{}) {
	if name != pluginNameE810 {
		glog.Errorf("Plugin must be initialized as '%s'", pluginNameE810)
		return nil, nil
	}
	_plugin, _data := NewIntelPlugin(pluginNameE810)
	_plugin.OnPTPConfigChange = OnPTPConfigChangeE810
	var iface interface{} = _data
	return _plugin, &iface
}
