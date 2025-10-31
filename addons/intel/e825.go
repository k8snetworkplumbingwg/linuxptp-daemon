package intel

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
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

var pluginNameE825 = "e825"

// E825Opts is the options structure for e825 plugin
type E825Opts struct {
	EnableDefaultConfig bool                         `json:"enableDefaultConfig"`
	UblxCmds            []E810UblxCmds               `json:"ublxCmds"`
	DevicePins          map[string]map[string]string `json:"pins"`
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
			for device, pins := range e825Opts.DevicePins {
				dpllClockIDStr := fmt.Sprintf("%s[%s]", dpll.ClockIdStr, device)
				if !unitTest {
					if zlErr == nil {
						(*nodeProfile).PtpSettings[dpllClockIDStr] = strconv.FormatUint(zlClockID, 10)
					} else {
						(*nodeProfile).PtpSettings[dpllClockIDStr] = strconv.FormatUint(getClockIDE825(device), 10)
					}
					for pin, value := range pins {
						deviceDir := fmt.Sprintf("/sys/class/net/%s/device/ptp/", device)
						phcs, pErr := os.ReadDir(deviceDir)
						if pErr != nil {
							glog.Error("e825 failed to read " + deviceDir + ": " + pErr.Error())
							continue
						}
						for _, phc := range phcs {
							pinPath := fmt.Sprintf("/sys/class/net/%s/device/ptp/%s/pins/%s", device, phc.Name(), pin)
							glog.Infof("echo %s > %s", value, pinPath)
							err = os.WriteFile(pinPath, []byte(value), 0666)
							if err != nil {
								glog.Error("e825 failed to write " + value + " to " + pinPath + ": " + err.Error())
							}
						}
					}
				}
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
						clockIDUsed = getClockIDE825(iface)
					}
					key := strings.Join([]string{iface, "phaseOffsetFilter", strconv.FormatUint(clockIDUsed, 10), pinProperty}, ".")
					(*nodeProfile).PtpSettings[key] = value
				}
			}
			if e825Opts.PhaseInputs != nil {
				if unitTest {
					// Mock clock chain DPLL pins in unit test
					clockChain.DpllPins = DpllPins
				}
				clockChain, err = InitClockChain(e825Opts, nodeProfile)
				if err != nil {
					return err
				}
				(*nodeProfile).PtpSettings["leadingInterface"] = clockChain.LeadingNIC.Name
				(*nodeProfile).PtpSettings["upstreamPort"] = clockChain.LeadingNIC.UpstreamPort
			} else {
				glog.Error("no clock chain set")
			}
		}
	}
	return nil
}

// AfterRunPTPCommandE825 performs actions after certain PTP commands for e825 plugin
func AfterRunPTPCommandE825(data *interface{}, nodeProfile *ptpv1.PtpProfile, command string) error {
	glog.Info("calling AfterRunPTPCommandE825 for e825 plugin")
	var e825Opts E825Opts
	var err error
	var optsByteArray []byte
	var stdout []byte

	e825Opts.EnableDefaultConfig = false

	for name, opts := range (*nodeProfile).Plugins {
		if name == "e825" {
			optsByteArray, _ = json.Marshal(opts)
			err = json.Unmarshal(optsByteArray, &e825Opts)
			if err != nil {
				glog.Error("e825 failed to unmarshal opts: " + err.Error())
			}
			switch command {
			case "gpspipe":
				glog.Infof("AfterRunPTPCommandE825 doing ublx config for command: %s", command)
				for _, ublxOpt := range append(e825Opts.UblxCmds, getDefaultUblxCmds()...) {
					ublxArgs := ublxOpt.Args
					glog.Infof("Running /usr/bin/ubxtool with args %s", strings.Join(ublxArgs, ", "))
					stdout, _ = exec.Command("/usr/local/bin/ubxtool", ublxArgs...).CombinedOutput()
					//stdout, err = exec.Command("/usr/local/bin/ubxtool", "-p", "STATUS").CombinedOutput()
					if data != nil && ublxOpt.ReportOutput {
						_data := *data
						glog.Infof("Saving status to hwconfig: %s", string(stdout))
						var pluginData = _data.(*E825PluginData)
						_pluginData := *pluginData
						statusString := fmt.Sprintf("ublx data: %s", string(stdout))
						*_pluginData.hwplugins = append(*_pluginData.hwplugins, statusString)
					} else {
						glog.Infof("Not saving status to hwconfig: %s", string(stdout))
					}
				}
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
		var pluginData = _data.(*E825PluginData)
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
	_plugin := plugin.Plugin{Name: "e825",
		OnPTPConfigChange:  OnPTPConfigChangeE825,
		AfterRunPTPCommand: AfterRunPTPCommandE825,
		PopulateHwConfig:   PopulateHwConfigE825,
	}
	var iface interface{} = &pluginData
	return &_plugin, &iface
}

func getClockIDE825(device string) uint64 {
	const (
		PCI_EXT_CAP_ID_DSN       = 3   //nolint
		PCI_CFG_SPACE_SIZE       = 256 //nolint
		PCI_EXT_CAP_NEXT_OFFSET  = 2   //nolint
		PCI_EXT_CAP_OFFSET_SHIFT = 4   //nolint
		PCI_EXT_CAP_DATA_OFFSET  = 4   //nolint
	)
	b, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/device/config", device))
	if err != nil {
		glog.Error(err)
		return 0
	}
	// Extended capability space starts right on PCI_CFG_SPACE
	var offset uint16 = PCI_CFG_SPACE_SIZE
	var id uint16
	for {
		id = binary.LittleEndian.Uint16(b[offset:])
		if id != PCI_EXT_CAP_ID_DSN {
			if id == 0 {
				glog.Errorf("can't find DSN for device %s", device)
				return 0
			}
			offset = binary.LittleEndian.Uint16(b[offset+PCI_EXT_CAP_NEXT_OFFSET:]) >> PCI_EXT_CAP_OFFSET_SHIFT
			continue
		}
		break
	}
	return binary.LittleEndian.Uint64(b[offset+PCI_EXT_CAP_DATA_OFFSET:])
}

// getClockIDByModule returns ClockID for a given DPLL module name, preferring PPS type if present
func getClockIDByModule(module string) (uint64, error) {
	if unitTest {
		return 0, fmt.Errorf("netlink disabled in unit test")
	}
	conn, err := dpll_netlink.Dial(nil)
	if err != nil {
		return 0, err
	}
	//nolint:errcheck
	defer conn.Close()
	devices, err := conn.DumpDeviceGet()
	if err != nil {
		return 0, err
	}
	var anyID uint64
	for _, d := range devices {
		if strings.EqualFold(d.ModuleName, module) {
			if d.Type == 1 { // PPS
				return d.ClockID, nil
			}
			anyID = d.ClockID
		}
	}
	if anyID != 0 {
		return anyID, nil
	}
	return 0, fmt.Errorf("module %s DPLL not found", module)
}
