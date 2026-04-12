package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/alias"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/network"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ptp4lconf"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/synce"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"

	"github.com/golang/glog"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

// Predefined config section names, re-exported from ptp4lconf for backward compatibility.
const (
	GlobalSectionName  = ptp4lconf.GlobalSectionName
	NmeaSectionName    = ptp4lconf.NmeaSectionName
	UnicastSectionName = ptp4lconf.UnicastSectionName
)

// LinuxPTPUpdate controls whether to update linuxPTP conf
// and contains linuxPTP conf to be updated. It's rendered
// and passed to linuxptp instance by daemon.
type LinuxPTPConfUpdate struct {
	UpdateCh               chan bool
	NodeProfiles           []ptpv1.PtpProfile
	appliedNodeProfileJSON []byte
	defaultPTP4lConfig     []byte
}

// TriggerRestartForHardwareChange implements HardwareConfigRestartTrigger interface
// This triggers the same restart mechanism used for PtpConfig changes
func (l *LinuxPTPConfUpdate) TriggerRestartForHardwareChange() error {
	glog.Info("Triggering PTP restart due to hardware configuration change")

	// Send the same signal that PtpConfig changes use
	select {
	case l.UpdateCh <- true:
		glog.Info("Successfully sent restart signal for hardware configuration change")
		return nil
	default:
		// Channel might be full, this shouldn't normally happen but handle gracefully
		glog.Warning("UpdateCh channel is full, restart signal may be delayed")
		go func() {
			l.UpdateCh <- true
		}()
		return nil
	}
}

// GetCurrentPTPProfiles implements HardwareConfigRestartTrigger interface
// Returns the names of currently active PTP profiles
func (l *LinuxPTPConfUpdate) GetCurrentPTPProfiles() []string {
	if l.NodeProfiles == nil {
		return []string{}
	}

	profileNames := make([]string, 0, len(l.NodeProfiles))
	for _, profile := range l.NodeProfiles {
		if profile.Name != nil {
			profileNames = append(profileNames, *profile.Name)
		}
	}

	glog.Infof("Current active PTP profiles: %v", profileNames)
	return profileNames
}

// Ptp4lConf wraps the shared ptp4lconf.Conf parser with daemon-specific
// fields (profile name, GNSS serial port) and rendering methods.
type Ptp4lConf struct {
	ptp4lconf.Conf
	profileName    string
	gnssSerialPort string
}

func (conf *Ptp4lConf) getPtp4lConfOptionOrEmptyString(sectionName, key string) (string, bool) {
	return conf.GetOption(sectionName, key)
}

func (conf *Ptp4lConf) setPtp4lConfOption(sectionName, key, value string, overwrite bool) {
	conf.SetOption(sectionName, key, value, overwrite)
}

func NewLinuxPTPConfUpdate() (*LinuxPTPConfUpdate, error) {
	if _, err := os.Stat(PTP4L_CONF_FILE_PATH); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("ptp.conf file doesn't exist")
		} else {
			return nil, fmt.Errorf("unknow error searching for the %s file: %v", PTP4L_CONF_FILE_PATH, err)
		}
	}

	defaultPTP4lConfig, err := os.ReadFile(PTP4L_CONF_FILE_PATH)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %v", PTP4L_CONF_FILE_PATH, err)
	}
	return &LinuxPTPConfUpdate{UpdateCh: make(chan bool, 10), defaultPTP4lConfig: defaultPTP4lConfig}, nil
}

// UpdateConfig updates the PTP configuration from the provided JSON.
// If ptpAuthUpdated is true, the configuration will be reapplied even if the JSON hasn't changed.
// This is used when security files (sa_file) have been updated and PTP processes need to restart.
func (l *LinuxPTPConfUpdate) UpdateConfig(nodeProfilesJSON []byte, ptpAuthUpdated bool) error {
	jsonChanged := !bytes.Equal(l.appliedNodeProfileJSON, nodeProfilesJSON)

	if !jsonChanged && !ptpAuthUpdated {
		glog.Info("UpdateConfig: config unchanged, skipping update")
		return nil
	}

	if ptpAuthUpdated {
		glog.Info("UpdateConfig: security files changed, forcing update")
	}

	if nodeProfiles, ok := tryToLoadConfig(nodeProfilesJSON); ok {
		glog.Infof("load profiles: %d profiles loaded", len(nodeProfiles))
		l.appliedNodeProfileJSON = nodeProfilesJSON
		l.NodeProfiles = nodeProfiles
		glog.Info("Sending update signal to daemon via UpdateCh")
		l.UpdateCh <- true
		glog.Info("Update signal sent successfully")

		return nil
	}

	if nodeProfiles, ok := tryToLoadOldConfig(nodeProfilesJSON); ok {
		// Support empty old config
		// '{"name":null,"interface":null}'
		if nodeProfiles[0].Name == nil || nodeProfiles[0].Interface == nil {
			glog.Infof("Skip no profile %+v", nodeProfiles[0])
			return nil
		}

		glog.Info("load profiles using old method")
		l.appliedNodeProfileJSON = nodeProfilesJSON
		l.NodeProfiles = nodeProfiles
		l.UpdateCh <- true

		return nil
	}

	return fmt.Errorf("unable to load profile config")
}

// Try to load the multiple policy config
func tryToLoadConfig(nodeProfilesJSON []byte) ([]ptpv1.PtpProfile, bool) {
	ptpConfig := []ptpv1.PtpProfile{}
	err := json.Unmarshal(nodeProfilesJSON, &ptpConfig)
	if err != nil {
		return nil, false
	}

	return ptpConfig, true
}

// For backward compatibility we also try to load the one policy scenario
func tryToLoadOldConfig(nodeProfilesJSON []byte) ([]ptpv1.PtpProfile, bool) {
	ptpConfig := &ptpv1.PtpProfile{}
	err := json.Unmarshal(nodeProfilesJSON, ptpConfig)
	if err != nil {
		return nil, false
	}

	return []ptpv1.PtpProfile{*ptpConfig}, true
}

// PopulatePtp4lConf delegates INI parsing and clock-type inference to ptp4lconf.Conf.Populate.
func (conf *Ptp4lConf) PopulatePtp4lConf(config *string, cliArgs *string) error {
	return conf.Populate(config, cliArgs)
}

// ExtendGlobalSection extends Ptp4lConf struct with fields not from ptp4lConf
func (conf *Ptp4lConf) ExtendGlobalSection(profileName string, messageTag string, socketPath string, pProcess string) {
	conf.profileName = profileName
	conf.setPtp4lConfOption(GlobalSectionName, "message_tag", messageTag, true)
	if socketPath != "" {
		conf.setPtp4lConfOption(GlobalSectionName, "uds_address", socketPath, true)
	}
	if gnssSerialPort, ok := conf.getPtp4lConfOptionOrEmptyString(GlobalSectionName, "ts2phc.nmea_serialport"); ok {
		conf.gnssSerialPort = strings.TrimSpace(gnssSerialPort)
		conf.setPtp4lConfOption(GlobalSectionName, "ts2phc.nmea_serialport", GPSPIPE_SERIALPORT, true)
	}
	if _, ok := conf.getPtp4lConfOptionOrEmptyString(GlobalSectionName, "leapfile"); ok || pProcess == ts2phcProcessName { // not required to check process if leapfile is always included
		conf.setPtp4lConfOption(GlobalSectionName, "leapfile", fmt.Sprintf("%s/%s", config.DefaultLeapConfigPath, os.Getenv("NODE_NAME")), true)
	}
}

// AddInterfaceSection adds interface to Ptp4lConf
func (conf *Ptp4lConf) AddInterfaceSection(iface string) {
	ifaceSectionName := fmt.Sprintf("[%s]", iface)
	conf.setPtp4lConfOption(ifaceSectionName, "", "", false)
}

func getSource(isTs2phcMaster string) event.EventSource {
	if ts2phcMaster, err := strconv.ParseBool(strings.TrimSpace(isTs2phcMaster)); err == nil {
		if ts2phcMaster {
			return event.GNSS
		}
	}
	return event.PPS
}

// extractSynceRelations extracts relation of synce device to interfaces
// The sections are ordered in the following way:
//  1. Device section specifies the configuration of a one logical device e.g. 'synce1'.
//     The name must be enclosed in extra angle bracket when defining new device section e.g. [<synce1>]
//     All ports defined by port sections AFTER the device section will create one SyncE device
//     (UNTIL next device section).
//  2. Port section - any other section not starting with < (e.g. [eth0]) is the port section.
//     Multiple port sections are allowed. Each port participates in SyncE communication.
func (conf *Ptp4lConf) extractSynceRelations() *synce.Relations {
	var err error
	r := &synce.Relations{
		Devices: []*synce.Config{},
	}

	ifaces := []string{}
	re, _ := regexp.Compile(`[{}<>\[\] ]+`)
	synceRelationInfo := synce.Config{}

	var extendedTlv, networkOption int = synce.ExtendedTLV_DISABLED, synce.SYNCE_NETWORK_OPT_1
	for _, section := range conf.Sections {
		sectionName := section.SectionName
		if strings.HasPrefix(sectionName, "[<") {
			if synceRelationInfo.Name != "" {
				if len(ifaces) > 0 {
					synceRelationInfo.Ifaces = ifaces
				}
				r.AddDeviceConfig(synceRelationInfo)
			}
			synceRelationInfo = synce.Config{
				Name:           "",
				Ifaces:         nil,
				ClockId:        "",
				NetworkOption:  synce.SYNCE_NETWORK_OPT_1,
				ExtendedTlv:    synce.ExtendedTLV_DISABLED,
				ExternalSource: "",
				LastQLState:    make(map[string]*synce.QualityLevelInfo),
				LastClockState: "",
			}
			extendedTlv, networkOption = synce.ExtendedTLV_DISABLED, synce.SYNCE_NETWORK_OPT_1

			synceRelationInfo.Name = re.ReplaceAllString(sectionName, "")
			if networkOptionStr, ok := conf.getPtp4lConfOptionOrEmptyString(sectionName, "network_option"); ok {
				if networkOption, err = strconv.Atoi(strings.TrimSpace(networkOptionStr)); err != nil {
					glog.Errorf("error parsing `network_option`, setting network_option to default 1 : %s", err)
				}
			}
			if extendedTlvStr, ok := conf.getPtp4lConfOptionOrEmptyString(sectionName, "extended_tlv"); ok {
				if extendedTlv, err = strconv.Atoi(strings.TrimSpace(extendedTlvStr)); err != nil {
					glog.Errorf("error parsing `extended_tlv`, setting extended_tlv to default 1 : %s", err)
				}
			}
			synceRelationInfo.NetworkOption = networkOption
			synceRelationInfo.ExtendedTlv = extendedTlv
		} else if strings.HasPrefix(sectionName, "[{") {
			synceRelationInfo.ExternalSource = re.ReplaceAllString(sectionName, "")
		} else if strings.HasPrefix(sectionName, "[") && sectionName != GlobalSectionName {
			iface := re.ReplaceAllString(sectionName, "")
			ifaces = append(ifaces, iface)
		}
	}
	if len(ifaces) > 0 {
		synceRelationInfo.Ifaces = ifaces
	}
	if synceRelationInfo.Name != "" {
		r.AddDeviceConfig(synceRelationInfo)
	}
	return r
}

// RenderSyncE4lConf outputs synce4l config as string
func (conf *Ptp4lConf) RenderSyncE4lConf(ptpSettings map[string]string) (configOut string, relations *synce.Relations) {
	configOut = fmt.Sprintf("#profile: %s\n", conf.profileName)
	relations = conf.extractSynceRelations()
	relations.AddClockIds(ptpSettings)
	deviceIdx := 0

	for i, section := range conf.Sections {
		configOut = fmt.Sprintf("%s\n%s", configOut, section.SectionName)
		if strings.HasPrefix(section.SectionName, "[<") {
			if _, found := conf.getPtp4lConfOptionOrEmptyString(section.SectionName, "clock_id"); !found {
				conf.setPtp4lConfOption(section.SectionName, "clock_id", relations.Devices[deviceIdx].ClockId, true)
				deviceIdx++
			}
		}
		for _, option := range conf.Sections[i].Options {
			configOut = fmt.Sprintf("%s\n%s %s", configOut, option.Key, option.Value)
		}
	}
	return
}

// RenderPtp4lConf outputs ptp4l config as string
func (conf *Ptp4lConf) RenderPtp4lConf() (configOut string, ifaces config.IFaces) {
	configOut = fmt.Sprintf("#profile: %s\n", conf.profileName)
	var nmea_source event.EventSource

	for _, section := range conf.Sections {
		configOut = fmt.Sprintf("%s\n%s", configOut, section.SectionName)

		if section.SectionName == NmeaSectionName {
			if source, ok := conf.getPtp4lConfOptionOrEmptyString(section.SectionName, "ts2phc.master"); ok {
				nmea_source = getSource(source)
			}
		}
		if section.SectionName != GlobalSectionName && section.SectionName != NmeaSectionName && section.SectionName != UnicastSectionName {
			iface := config.Iface{Name: ptp4lconf.SectionName(section.SectionName)}
			iface.PhcId = network.GetPhcId(iface.Name)

			if source, ok := conf.getPtp4lConfOptionOrEmptyString(section.SectionName, "ts2phc.master"); ok {
				iface.Source = getSource(source)
			} else {
				iface.Source = nmea_source
			}
			if masterOnly, ok := conf.getPtp4lConfOptionOrEmptyString(section.SectionName, "masterOnly"); ok {
				// TODO add error handling
				iface.IsMaster, _ = strconv.ParseBool(strings.TrimSpace(masterOnly))
			}
			ifaces = append(ifaces, iface)
			alias.AddInterface(iface.PhcId, iface.Name)
		}
		for _, option := range section.Options {
			configOut = fmt.Sprintf("%s\n%s %s", configOut, option.Key, option.Value)
		}
	}
	alias.CalculateAliases()
	return configOut, ifaces
}
