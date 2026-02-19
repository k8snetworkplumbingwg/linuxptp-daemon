package plugin

import (
	"context"
	"fmt"
	"time"

	"github.com/golang/glog"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// Plugin type
type Plugin struct {
	Name                   string
	Options                interface{}
	OnPTPConfigChange      OnPTPConfigChange
	AfterRunPTPCommand     AfterRunPTPCommand
	PopulateHwConfig       PopulateHwConfig
	RegisterEnableCallback RegisterEnableCallback
	ProcessLog             ProcessLog
}

// PluginManager type
type PluginManager struct { //nolint:revive
	Plugins map[string]*Plugin
	Data    map[string]*interface{}
	// RestConfig is used to create dynamic client for PtpConfig status updates
	// when reporting hardware plugin configuration errors
	RestConfig *rest.Config
	// Namespace is the namespace where PtpConfig resources are located
	Namespace string
}

// New type
type New func(string) (*Plugin, *interface{})

// OnPTPConfigChange type
type OnPTPConfigChange func(*interface{}, *ptpv1.PtpProfile) error

// PopulateHwConfig type
type PopulateHwConfig func(*interface{}, *[]ptpv1.HwConfig) error

// AfterRunPTPCommand type
type AfterRunPTPCommand func(*interface{}, *ptpv1.PtpProfile, string) error

// RegisterEnableCallback type
type RegisterEnableCallback func(*interface{}, string, func(bool))

// ProcessLog type
type ProcessLog func(*interface{}, string, string) string

// OnPTPConfigChange is plugin interface
func (pm *PluginManager) OnPTPConfigChange(nodeProfile *ptpv1.PtpProfile) {
	for pluginName, pluginObject := range pm.Plugins {
		pluginFunc := pluginObject.OnPTPConfigChange
		if pluginFunc != nil {
			pluginFunc(pm.Data[pluginName], nodeProfile)
		}
	}
}

// AfterRunPTPCommand is plugin interface
func (pm *PluginManager) AfterRunPTPCommand(nodeProfile *ptpv1.PtpProfile, command string) {
	for pluginName, pluginObject := range pm.Plugins {
		pluginFunc := pluginObject.AfterRunPTPCommand
		if pluginFunc != nil {
			pluginFunc(pm.Data[pluginName], nodeProfile, command)
		}
	}
}

// PopulateHwConfig is plugin interface
func (pm *PluginManager) PopulateHwConfig(hwconfigs *[]ptpv1.HwConfig) {
	for pluginName, pluginObject := range pm.Plugins {
		pluginFunc := pluginObject.PopulateHwConfig
		if pluginFunc != nil {
			pluginFunc(pm.Data[pluginName], hwconfigs)
		}
	}
}

// RegisterEnableCallback is plugin interface
func (pm *PluginManager) RegisterEnableCallback(pname string, cmdSetEnabled func(bool)) {
	for pluginName, pluginObject := range pm.Plugins {
		pluginFunc := pluginObject.RegisterEnableCallback
		if pluginFunc != nil {
			pluginFunc(pm.Data[pluginName], pname, cmdSetEnabled)
		}
	}
}

// ProcessLog is plugin interface
func (pm *PluginManager) ProcessLog(pname string, log string) string {
	ret := log

	for pluginName, pluginObject := range pm.Plugins {
		pluginFunc := pluginObject.ProcessLog
		if pluginFunc != nil {
			pluginData := pm.Data
			pluginDataName := pluginData[pluginName]
			ret = pluginFunc(pluginDataName, pname, ret)
		}
	}
	return ret
}

// ValidateAndReportPluginErrors validates hardware plugin names in profiles and reports
// errors to PtpConfig status. This detects typos in the hardware plugin configuration
// section (e.g., "e81" instead of "e810") and reports them via the PtpConfig status.
// When plugins are corrected, this also clears the warning condition.
// This method should be called after profiles are applied to ensure status is updated.
func (pm *PluginManager) ValidateAndReportPluginErrors(profiles []ptpv1.PtpProfile) {
	if pm.RestConfig == nil {
		glog.V(2).Infof("RestConfig not available in PluginManager, skipping hardware plugin validation")
		return
	}

	// Track profiles with errors vs valid profiles
	profilesWithErrors := make(map[string]string) // profileName -> error message
	profilesValid := make(map[string]bool)        // profileName -> true (valid plugins)

	// Get valid plugin names from registered plugins
	var validNames []string
	for name := range pm.Plugins {
		validNames = append(validNames, name)
	}

	// Check each profile for unrecognized plugins
	for _, profile := range profiles {
		profileName := ""
		if profile.Name != nil {
			profileName = *profile.Name
		}

		if len(profile.Plugins) == 0 {
			// No plugins configured - mark as valid (no warning needed)
			profilesValid[profileName] = true
			continue
		}

		// Validate plugin names against registered plugins
		var unrecognized []string
		for pluginName := range profile.Plugins {
			if _, exists := pm.Plugins[pluginName]; !exists {
				unrecognized = append(unrecognized, pluginName)
			}
		}

		if len(unrecognized) == 0 {
			// All plugins are valid - mark for clearing any existing warning
			profilesValid[profileName] = true
			continue
		}

		// Build error message
		errMsg := fmt.Sprintf("Profile '%s' contains unrecognized hardware plugin(s): %v. Valid plugins are: %v",
			profileName, unrecognized, validNames)
		glog.Warningf("Plugin validation error: %s", errMsg)
		profilesWithErrors[profileName] = errMsg
	}

	// Update PtpConfig status for profiles with errors
	for profileName, errMsg := range profilesWithErrors {
		pm.updatePtpConfigStatusWithPluginError(profileName, errMsg)
	}

	// Clear warning condition for profiles that are now valid
	for profileName := range profilesValid {
		pm.clearPtpConfigPluginWarning(profileName)
	}
}

// updatePtpConfigStatusWithPluginError finds the PtpConfig containing the specified profile
// and updates its status with a hardware plugin configuration warning.
func (pm *PluginManager) updatePtpConfigStatusWithPluginError(profileName, errMsg string) {
	ctx := context.Background()

	dynClient, err := dynamic.NewForConfig(pm.RestConfig)
	if err != nil {
		glog.Errorf("Failed to create dynamic client for PtpConfig status update: %v", err)
		return
	}

	ptpConfigGVR := schema.GroupVersionResource{
		Group:    "ptp.openshift.io",
		Version:  "v1",
		Resource: "ptpconfigs",
	}

	// List all PtpConfigs to find the one containing this profile
	ptpConfigList, err := dynClient.Resource(ptpConfigGVR).Namespace(pm.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		glog.Errorf("Failed to list PtpConfigs: %v", err)
		return
	}

	for _, item := range ptpConfigList.Items {
		// Check if this PtpConfig contains the profile
		profiles, found, _ := unstructured.NestedSlice(item.Object, "spec", "profile")
		if !found {
			continue
		}

		for _, p := range profiles {
			profileMap, ok := p.(map[string]interface{})
			if !ok {
				continue
			}
			name, _, _ := unstructured.NestedString(profileMap, "name")
			if name == profileName {
				// Found the PtpConfig containing this profile, update its status
				pm.setPtpConfigStatusCondition(ctx, dynClient, ptpConfigGVR, item.GetNamespace(), item.GetName(), errMsg)
				return
			}
		}
	}

	glog.Warningf("Could not find PtpConfig containing profile '%s' to report plugin error", profileName)
}

// setPtpConfigStatusCondition updates the status.conditions of a PtpConfig with a warning
func (pm *PluginManager) setPtpConfigStatusCondition(ctx context.Context, dynClient dynamic.Interface, gvr schema.GroupVersionResource, namespace, name, errMsg string) {
	// Get current resource
	resource, err := dynClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		glog.Errorf("Failed to get PtpConfig %s/%s for status update: %v", namespace, name, err)
		return
	}

	// Build the condition
	condition := map[string]interface{}{
		"type":               "HardwarePluginConfigurationWarning",
		"status":             "True",
		"reason":             "InvalidPluginName",
		"message":            errMsg,
		"lastTransitionTime": time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}

	// Get existing conditions
	conditions, _, _ := unstructured.NestedSlice(resource.Object, "status", "conditions")

	// Update existing condition or add new one
	conditionFound := false
	for i, c := range conditions {
		if condMap, ok := c.(map[string]interface{}); ok {
			if condMap["type"] == "HardwarePluginConfigurationWarning" {
				conditions[i] = condition
				conditionFound = true
				break
			}
		}
	}
	if !conditionFound {
		conditions = append(conditions, condition)
	}

	// Set the conditions back
	if setErr := unstructured.SetNestedSlice(resource.Object, conditions, "status", "conditions"); setErr != nil {
		glog.Errorf("Failed to set conditions in PtpConfig %s/%s: %v", namespace, name, setErr)
		return
	}

	// Update the status subresource
	_, err = dynClient.Resource(gvr).Namespace(namespace).UpdateStatus(ctx, resource, metav1.UpdateOptions{})
	if err != nil {
		glog.Errorf("Failed to update PtpConfig %s/%s status with plugin validation error: %v", namespace, name, err)
	} else {
		glog.Infof("Updated PtpConfig '%s/%s' status with hardware plugin configuration warning", namespace, name)
	}
}

// clearPtpConfigPluginWarning removes the HardwarePluginConfigurationWarning condition
// from a PtpConfig when all plugins are now valid (i.e., user fixed the typo).
func (pm *PluginManager) clearPtpConfigPluginWarning(profileName string) {
	ctx := context.Background()

	dynClient, err := dynamic.NewForConfig(pm.RestConfig)
	if err != nil {
		glog.V(2).Infof("Failed to create dynamic client for clearing plugin warning: %v", err)
		return
	}

	ptpConfigGVR := schema.GroupVersionResource{
		Group:    "ptp.openshift.io",
		Version:  "v1",
		Resource: "ptpconfigs",
	}

	// List all PtpConfigs to find the one containing this profile
	ptpConfigList, err := dynClient.Resource(ptpConfigGVR).Namespace(pm.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		glog.V(2).Infof("Failed to list PtpConfigs for clearing warning: %v", err)
		return
	}

	for _, item := range ptpConfigList.Items {
		// Check if this PtpConfig contains the profile
		profiles, found, _ := unstructured.NestedSlice(item.Object, "spec", "profile")
		if !found {
			continue
		}

		for _, p := range profiles {
			profileMap, ok := p.(map[string]interface{})
			if !ok {
				continue
			}
			name, _, _ := unstructured.NestedString(profileMap, "name")
			if name == profileName {
				// Found the PtpConfig - check if it has the warning condition and remove it
				pm.removePluginWarningCondition(ctx, dynClient, ptpConfigGVR, item.GetNamespace(), item.GetName())
				return
			}
		}
	}
}

// removePluginWarningCondition removes the HardwarePluginConfigurationWarning condition from status
func (pm *PluginManager) removePluginWarningCondition(ctx context.Context, dynClient dynamic.Interface, gvr schema.GroupVersionResource, namespace, name string) {
	// Get current resource
	resource, err := dynClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		glog.V(2).Infof("Failed to get PtpConfig %s/%s for clearing warning: %v", namespace, name, err)
		return
	}

	// Get existing conditions
	conditions, found, _ := unstructured.NestedSlice(resource.Object, "status", "conditions")
	if !found || len(conditions) == 0 {
		// No conditions to remove
		return
	}

	// Filter out the HardwarePluginConfigurationWarning condition
	var newConditions []interface{}
	warningFound := false
	for _, c := range conditions {
		if condMap, ok := c.(map[string]interface{}); ok {
			if condMap["type"] == "HardwarePluginConfigurationWarning" {
				warningFound = true
				continue // Skip this condition (remove it)
			}
		}
		newConditions = append(newConditions, c)
	}

	if !warningFound {
		// No warning condition to remove
		return
	}

	// Set the filtered conditions back
	if setErr := unstructured.SetNestedSlice(resource.Object, newConditions, "status", "conditions"); setErr != nil {
		glog.Errorf("Failed to remove warning condition from PtpConfig %s/%s: %v", namespace, name, setErr)
		return
	}

	// Update the status subresource
	_, err = dynClient.Resource(gvr).Namespace(namespace).UpdateStatus(ctx, resource, metav1.UpdateOptions{})
	if err != nil {
		glog.V(2).Infof("Failed to update PtpConfig %s/%s status when clearing warning: %v", namespace, name, err)
	} else {
		glog.Infof("Cleared hardware plugin configuration warning from PtpConfig '%s/%s'", namespace, name)
	}
}

// ValidateProfilePlugins checks if all plugin names in the profile are valid.
// Returns a slice of unrecognized plugin names (empty if all are valid).
// This is used to detect typos in the hardware plugin configuration section
// of PtpConfig and report them to the PtpConfig status.
// The validPlugins map should contain all valid plugin names as keys (e.g., from mapping.PluginMapping).
func ValidateProfilePlugins(profile *ptpv1.PtpProfile, validPlugins map[string]New) []string {
	var unrecognized []string
	if profile == nil || profile.Plugins == nil {
		return unrecognized
	}
	for pluginName := range profile.Plugins {
		if _, exists := validPlugins[pluginName]; !exists {
			unrecognized = append(unrecognized, pluginName)
		}
	}
	return unrecognized
}
