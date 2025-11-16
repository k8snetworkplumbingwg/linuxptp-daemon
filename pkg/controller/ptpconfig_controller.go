package controller

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/golang/glog"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/daemon"
)

// PtpConfigReconciler reconciles PtpConfig objects and provides configuration updates to the daemon
type PtpConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// NodeName is the name of the node this daemon is running on
	NodeName string

	// ConfigUpdate channel to send configuration updates to the daemon
	ConfigUpdate *daemon.LinuxPTPConfUpdate
}

// +kubebuilder:rbac:groups=ptp.openshift.io,resources=ptpconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=ptp.openshift.io,resources=ptpconfigs/status,verbs=get;update;patch

// Reconcile handles PtpConfig changes and updates the daemon configuration
func (r *PtpConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.Info("Reconciling PtpConfig", "name", req.Name, "namespace", req.Namespace)

	// Get the PtpConfig resource
	ptpConfig := &ptpv1.PtpConfig{}
	err := r.Get(ctx, req.NamespacedName, ptpConfig)
	if err != nil {
		if errors.IsNotFound(err) {
			// PtpConfig was deleted, trigger recalculation of node profiles
			log.Info("PtpConfig deleted, recalculating node profiles", "name", req.Name)
			return r.reconcileAllConfigs(ctx)
		}
		log.Error(err, "Failed to get PtpConfig")
		return ctrl.Result{}, err
	}

	// Recalculate and apply configuration for this node
	return r.reconcileAllConfigs(ctx)
}

// reconcileAllConfigs calculates the effective configuration for this node by examining all PtpConfigs
func (r *PtpConfigReconciler) reconcileAllConfigs(ctx context.Context) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// List all PtpConfigs in the cluster
	ptpConfigList := &ptpv1.PtpConfigList{}
	if err := r.List(ctx, ptpConfigList); err != nil {
		log.Error(err, "Failed to list PtpConfigs")
		return ctrl.Result{}, err
	}

	// Calculate the matching profiles for this node
	matchingProfiles, err := r.calculateNodeProfiles(ctx, ptpConfigList.Items)
	if err != nil {
		log.Error(err, "Failed to calculate node profiles")
		return ctrl.Result{}, err
	}

	// Convert profiles to JSON and update the daemon configuration
	if len(matchingProfiles) > 0 {
		nodeProfilesJSON, err1 := json.Marshal(matchingProfiles)
		if err1 != nil {
			log.Error(err, "Failed to marshal node profiles")
			return ctrl.Result{}, err1
		}

		glog.Infof("Updating daemon configuration with %d profiles for node %s", len(matchingProfiles), r.NodeName)

		// Send configuration update to daemon
		err = r.ConfigUpdate.UpdateConfig(nodeProfilesJSON)
		if err != nil {
			log.Error(err, "Failed to update daemon configuration")
			return ctrl.Result{}, err
		}

		log.Info("Successfully updated daemon configuration", "profiles", len(matchingProfiles))
	} else {
		glog.Infof("No matching profiles found for node %s", r.NodeName)

		// Send empty configuration to clear any existing config
		err = r.ConfigUpdate.UpdateConfig([]byte("[]"))
		if err != nil {
			log.Error(err, "Failed to clear daemon configuration")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// calculateNodeProfiles determines which PTP profiles should be applied to this node
func (r *PtpConfigReconciler) calculateNodeProfiles(ctx context.Context, ptpConfigs []ptpv1.PtpConfig) ([]ptpv1.PtpProfile, error) {
	log := log.FromContext(ctx)

	// Get node labels for matching
	node := &corev1.Node{}
	err := r.Get(ctx, types.NamespacedName{Name: r.NodeName}, node)
	if err != nil {
		return nil, fmt.Errorf("failed to get node %s: %w", r.NodeName, err)
	}

	// First, collect all matching recommendations across all configs
	type matchedRecommendation struct {
		recommend  ptpv1.PtpRecommend
		profile    ptpv1.PtpProfile
		priority   int64
		configName string
	}

	var allMatches []matchedRecommendation

	// Process each PtpConfig and find ALL matching recommendations
	for _, ptpConfig := range ptpConfigs {
		log.Info("Processing PtpConfig", "name", ptpConfig.Name, "profiles", len(ptpConfig.Spec.Profile), "recommendations", len(ptpConfig.Spec.Recommend))

		for _, recommend := range ptpConfig.Spec.Recommend {
			if r.doesRecommendationMatch(recommend, r.NodeName, node.Labels) {
				priority := int64(0)
				if recommend.Priority != nil {
					priority = *recommend.Priority
				}

				// Find the corresponding profile
				if recommend.Profile != nil {
					profileName := *recommend.Profile
					for _, profile := range ptpConfig.Spec.Profile {
						if profile.Name != nil && *profile.Name == profileName {
							allMatches = append(allMatches, matchedRecommendation{
								recommend:  recommend,
								profile:    profile,
								priority:   priority,
								configName: ptpConfig.Name,
							})
							log.Info("Found matching recommendation", "config", ptpConfig.Name, "profile", profileName, "priority", priority)
							break
						}
					}
				}
			}
		}
	}

	// Now select only the highest priority match(es)
	var matchingProfiles []ptpv1.PtpProfile
	if len(allMatches) > 0 {
		// Find the highest priority
		maxPriority := allMatches[0].priority
		for _, match := range allMatches {
			if match.priority > maxPriority {
				maxPriority = match.priority
			}
		}

		log.Info("Found maximum priority", "priority", maxPriority, "totalMatches", len(allMatches))

		// Select all profiles with the highest priority
		for _, match := range allMatches {
			if match.priority == maxPriority {
				matchingProfiles = append(matchingProfiles, match.profile)
				log.Info("Selected profile", "config", match.configName, "profile", *match.profile.Name, "priority", match.priority)
			}
		}
	}

	return matchingProfiles, nil
}

// doesRecommendationMatch checks if a recommendation matches the current node
func (r *PtpConfigReconciler) doesRecommendationMatch(recommend ptpv1.PtpRecommend, nodeName string, nodeLabels map[string]string) bool {
	// If no match rules are specified, it matches all nodes
	if len(recommend.Match) == 0 {
		return true
	}

	// Check each match rule - any match rule can match (OR logic)
	for _, matchRule := range recommend.Match {
		// Check node name match
		if matchRule.NodeName != nil && *matchRule.NodeName == nodeName {
			return true
		}

		// Check node label match
		if matchRule.NodeLabel != nil {
			labelKey := *matchRule.NodeLabel
			// For label matching, we check if the label exists on the node
			// The format is expected to be "key=value" or just "key"
			if _, exists := nodeLabels[labelKey]; exists {
				// If it's just a key, any value matches
				return true
			}
			// TODO: Add support for "key=value" format matching
		}
	}

	return false
}

// SetupWithManager sets up the controller with the Manager
func (r *PtpConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Watch PtpConfig resources
	return ctrl.NewControllerManagedBy(mgr).
		For(&ptpv1.PtpConfig{}).
		WithOptions(controller.Options{
			// Use a custom reconciler name for logging
			RecoverPanic: func() *bool { v := true; return &v }(),
		}).
		Complete(r)
}
