package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/synce"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"

	"github.com/golang/glog"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

// predefined config section names
const (
	GlobalSectionName  = "[global]"
	NmeaSectionName    = "[nmea]"
	UnicastSectionName = "[unicast_master_table]"
)

// LinuxPTPUpdate controls whether to update linuxPTP conf
// and contains linuxPTP conf to be updated. It's rendered
// and passed to linuxptp instance by daemon.
type LinuxPTPConfUpdate struct {
	UpdateCh               chan bool
	NodeProfiles           []ptpv1.PtpProfile
	appliedNodeProfileJson []byte
	defaultPTP4lConfig     []byte
}

type ptp4lConfOption struct {
	key   string
	value string
}

type ptp4lConfSection struct {
	options []ptp4lConfOption
}

type ptp4lConf struct {
	sectionOptions   map[string][]ptp4lConfOption
	profile_name     string
	clock_type       event.ClockType
	gnss_serial_port string // gnss serial port
}

func (conf *ptp4lConf) getPtp4lConfOptionOrEmptyString(sectionName string, key string) (string, bool) {
	for _, option := range conf.sectionOptions[sectionName] {
		if option.key == key {
			return option.value, true
		}
	}
	return "", false
}

func (conf *ptp4lConf) setPtp4lConfOption(sectionName string, key string, value string, overwrite bool) {
	_, ok := conf.sectionOptions[sectionName]
	if !ok {
		conf.sectionOptions[sectionName] = make([]ptp4lConfOption, 0)
	}
	if key == "" {
		return
	}
	if overwrite {
		for i := range conf.sectionOptions[sectionName] {
			if conf.sectionOptions[sectionName][i].key == key {
				conf.sectionOptions[sectionName][i] = ptp4lConfOption{key: key, value: value}
			}
		}
	} else {
		conf.sectionOptions[sectionName] = append(conf.sectionOptions[sectionName], ptp4lConfOption{key: key, value: value})
	}
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

	return &LinuxPTPConfUpdate{UpdateCh: make(chan bool), defaultPTP4lConfig: defaultPTP4lConfig}, nil
}

func (l *LinuxPTPConfUpdate) UpdateConfig(nodeProfilesJson []byte) error {
	if string(l.appliedNodeProfileJson) == string(nodeProfilesJson) {
		return nil
	}
	if nodeProfiles, ok := tryToLoadConfig(nodeProfilesJson); ok {
		glog.Info("load profiles")
		l.appliedNodeProfileJson = nodeProfilesJson
		l.NodeProfiles = nodeProfiles
		l.UpdateCh <- true

		return nil
	}

	if nodeProfiles, ok := tryToLoadOldConfig(nodeProfilesJson); ok {
		// Support empty old config
		// '{"name":null,"interface":null}'
		if nodeProfiles[0].Name == nil || nodeProfiles[0].Interface == nil {
			glog.Infof("Skip no profile %+v", nodeProfiles[0])
			return nil
		}

		glog.Info("load profiles using old method")
		l.appliedNodeProfileJson = nodeProfilesJson
		l.NodeProfiles = nodeProfiles
		l.UpdateCh <- true

		return nil
	}

	return fmt.Errorf("unable to load profile config")
}

// Try to load the multiple policy config
func tryToLoadConfig(nodeProfilesJson []byte) ([]ptpv1.PtpProfile, bool) {
	ptpConfig := []ptpv1.PtpProfile{}
	err := json.Unmarshal(nodeProfilesJson, &ptpConfig)
	if err != nil {
		return nil, false
	}

	return ptpConfig, true
}

// For backward compatibility we also try to load the one policy scenario
func tryToLoadOldConfig(nodeProfilesJson []byte) ([]ptpv1.PtpProfile, bool) {
	ptpConfig := &ptpv1.PtpProfile{}
	err := json.Unmarshal(nodeProfilesJson, ptpConfig)
	if err != nil {
		return nil, false
	}

	return []ptpv1.PtpProfile{*ptpConfig}, true
}

// Takes as input a PtpProfile.Ptp4lConf and outputs as ptp4lConf struct
func (conf *ptp4lConf) populatePtp4lConf(config *string) error {
	var currentSectionName string
	conf.sectionOptions = make(map[string][]ptp4lConfOption)
	hasSlaveConfigDefined := false
	ifaceCount := 0
	if config != nil {
		for _, line := range strings.Split(*config, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "#") {
				continue
			} else if strings.HasPrefix(line, "[") {
				currentLine := strings.Split(line, "]")
				if len(currentLine) < 2 {
					return errors.New("Section missing closing ']': " + line)
				}
				currentSectionName = fmt.Sprintf("%s]", currentLine[0])
				if currentSectionName != GlobalSectionName && currentSectionName != NmeaSectionName && currentSectionName != UnicastSectionName {
					ifaceCount++
				}
				conf.setPtp4lConfOption(currentSectionName, "", "", false)
			} else if currentSectionName != "" {
				split := strings.IndexByte(line, ' ')
				if split > 0 {
					key := line[:split]
					value := line[split:]
					conf.setPtp4lConfOption(currentSectionName, key, value, false)
					if (key == "masterOnly" && value == "0") ||
						(key == "serverOnly" && value == "0") ||
						(key == "slaveOnly" && value == "1") ||
						(key == "clientOnly" && value == "1") {
						hasSlaveConfigDefined = true
					}
				}
			} else {
				return errors.New("Config option not in section: " + line)
			}
		}
	}

	if !hasSlaveConfigDefined {
		// No Slave Interfaces defined
		conf.clock_type = event.GM
	} else if ifaceCount > 1 {
		// Multiple interfaces with at least one slave Interface defined
		conf.clock_type = event.BC
	} else {
		// Single slave Interface defined
		conf.clock_type = event.OC
	}

	return nil
}

func (conf *ptp4lConf) extendGlobalSection(messageTag string, socketPath string, pProcess string) {
	conf.setPtp4lConfOption(GlobalSectionName, "message_tag", messageTag, true)
	if socketPath != "" {
		conf.setPtp4lConfOption(GlobalSectionName, "uds_address", socketPath, true)
	}
	if gnssSerialPort, ok := conf.getPtp4lConfOptionOrEmptyString(GlobalSectionName, "ts2phc.nmea_serialport"); ok {
		conf.gnss_serial_port = strings.TrimSpace(gnssSerialPort)
		conf.setPtp4lConfOption(GlobalSectionName, "ts2phc.nmea_serialport", GPSPIPE_SERIALPORT, true)
	}
	if _, ok := conf.getPtp4lConfOptionOrEmptyString(GlobalSectionName, "leapfile"); ok || pProcess == ts2phcProcessName { // not required to check process if leapfile is always included
		conf.setPtp4lConfOption(GlobalSectionName, "leapfile", fmt.Sprintf("%s/%s", config.DefaultLeapConfigPath, os.Getenv("NODE_NAME")), true)
	}
}

func (conf *ptp4lConf) addInterfaceSection(iface string) {
	ifaceSectionName := fmt.Sprintf("[%s]", iface)
	_, ok := conf.sectionOptions[ifaceSectionName]
	if !ok {
		conf.sectionOptions[ifaceSectionName] = make([]ptp4lConfOption, 0)
	}
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
func (conf *ptp4lConf) extractSynceRelations() *synce.Relations {
	var err error
	r := &synce.Relations{
		Devices: []*synce.Config{},
	}

	ifaces := []string{}
	re, _ := regexp.Compile(`[{}<>\[\] ]+`)
	synceRelationInfo := synce.Config{}

	var extendedTlv, networkOption int = synce.ExtendedTLV_DISABLED, synce.SYNCE_NETWORK_OPT_1
	for sectionName := range conf.sectionOptions {
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

func (conf *ptp4lConf) renderSyncE4lConf(ptpSettings map[string]string) (configOut string, relations *synce.Relations) {
	configOut = fmt.Sprintf("#profile: %s\n", conf.profile_name)
	relations = conf.extractSynceRelations()
	relations.AddClockIds(ptpSettings)
	deviceIdx := 0
	for sectionName, sectionOptions := range conf.sectionOptions {
		configOut = fmt.Sprintf("%s\n%s", configOut, sectionName)
		if strings.HasPrefix(sectionName, "[<") {
			if _, found := conf.getPtp4lConfOptionOrEmptyString(sectionName, "clock_id"); !found {
				conf.setPtp4lConfOption(sectionName, "clock_id", relations.Devices[deviceIdx].ClockId, true)
				deviceIdx++
			}
		}
		for _, option := range sectionOptions {
			k := option.key
			v := option.value
			configOut = fmt.Sprintf("%s\n%s %s", configOut, k, v)
		}
	}
	return
}

func (conf *ptp4lConf) renderPtp4lConf() (configOut string, ifaces config.IFaces) {
	configOut = fmt.Sprintf("#profile: %s\n", conf.profile_name)
	var nmea_source event.EventSource

	for sectionName, sectionOptions := range conf.sectionOptions {
		configOut = fmt.Sprintf("%s\n%s", configOut, sectionName)

		if sectionName == NmeaSectionName {
			if source, ok := conf.getPtp4lConfOptionOrEmptyString(sectionName, "ts2phc.master"); ok {
				nmea_source = getSource(source)
			}
		}
		if sectionName != GlobalSectionName && sectionName != NmeaSectionName && sectionName != UnicastSectionName {
			i := sectionName
			i = strings.ReplaceAll(i, "[", "")
			i = strings.ReplaceAll(i, "]", "")
			iface := config.Iface{Name: i}
			if source, ok := conf.getPtp4lConfOptionOrEmptyString(sectionName, "ts2phc.master"); ok {
				iface.Source = getSource(source)
			} else {
				// if not defined here, use source defined at nmea section
				iface.Source = nmea_source
			}
			if masterOnly, ok := conf.getPtp4lConfOptionOrEmptyString(sectionName, "masterOnly"); ok {
				// TODO add error handling
				iface.IsMaster, _ = strconv.ParseBool(strings.TrimSpace(masterOnly))
			}
			ifaces = append(ifaces, config.Iface{
				Name:   iface.Name,
				Source: iface.Source,
				PhcId:  iface.PhcId,
			})
		}
		for _, option := range sectionOptions {
			k := option.key
			v := option.value
			configOut = fmt.Sprintf("%s\n%s %s", configOut, k, v)
		}
	}
	return configOut, ifaces
}
