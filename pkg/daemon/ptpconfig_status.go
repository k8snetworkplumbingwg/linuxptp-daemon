package daemon

import (
	"context"

	"github.com/golang/glog"
	ptpclient "github.com/k8snetworkplumbingwg/ptp-operator/pkg/client/clientset/versioned"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// ConditionTypeHardwarePluginReady indicates whether hardware plugin configuration was applied successfully
	ConditionTypeHardwarePluginReady = "HardwarePluginReady"
)

// FindPtpConfigByProfileName lists all PtpConfigs and returns the name/namespace
// of the one containing a profile with the given name.
func FindPtpConfigByProfileName(ptpClient *ptpclient.Clientset, namespace, profileName string) (string, string, bool) {
	ptpConfigs, err := ptpClient.PtpV1().PtpConfigs(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		glog.Errorf("Failed to list PtpConfigs: %v", err)
		return "", "", false
	}
	for _, cfg := range ptpConfigs.Items {
		for _, p := range cfg.Spec.Profile {
			if p.Name != nil && *p.Name == profileName {
				return cfg.Name, cfg.Namespace, true
			}
		}
	}
	return "", "", false
}

// UpdatePtpConfigCondition updates a condition on the given PtpConfig's status.
func UpdatePtpConfigCondition(ptpClient *ptpclient.Clientset, namespace, configName string, condType string, status metav1.ConditionStatus, reason, message string) {
	ptpConfig, err := ptpClient.PtpV1().PtpConfigs(namespace).Get(context.TODO(), configName, metav1.GetOptions{})
	if err != nil {
		glog.Errorf("Failed to get PtpConfig %s/%s for status update: %v", namespace, configName, err)
		return
	}

	newCondition := metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}

	meta.SetStatusCondition(&ptpConfig.Status.Conditions, newCondition)

	_, err = ptpClient.PtpV1().PtpConfigs(namespace).UpdateStatus(context.TODO(), ptpConfig, metav1.UpdateOptions{})
	if err != nil {
		glog.Errorf("Failed to update PtpConfig %s/%s status: %v", namespace, configName, err)
	} else {
		glog.Infof("Updated PtpConfig %s/%s condition %s=%s reason=%s", namespace, configName, condType, status, reason)
	}
}
