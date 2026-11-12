package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/golang/glog"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
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
