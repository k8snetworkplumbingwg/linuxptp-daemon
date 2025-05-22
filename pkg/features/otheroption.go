package features

import (
	"github.com/golang/glog"
)

// FeatureFlag ...
type FeatureFlag int

// Feature flag enum ...
const (
	FeatureDualBC FeatureFlag = iota
	FeatureGM
	FeatureLeapFile
	FeatureLogSeverity
	FeatureGMHoldover
	FeatureSyncE
	FeatureHA
	FeatureBCHoldover
)

// IsFeatureAvaiable ...
func (f *Features) IsFeatureAvaiable(feature FeatureFlag) bool {
	switch feature {
	case FeatureDualBC:
		return f.dualBC
	case FeatureGM:
		return f.gm
	case FeatureLeapFile:
		return f.leapFile
	case FeatureLogSeverity:
		return f.logSeverity
	case FeatureGMHoldover:
		return f.gmHoldover
	case FeatureSyncE:
		return f.syncE
	case FeatureHA:
		return f.ha
	case FeatureBCHoldover:
		return f.bcHoldover
	default:
		glog.Errorf("Unknown feature flag %d", feature)
		return false
	}
}

// AreFeaturesAvaiable ...
func (f *Features) AreFeaturesAvaiable(features ...FeatureFlag) bool {
	isAvailable := true
	for _, flag := range features {
		isAvailable = isAvailable && f.IsFeatureAvaiable(flag)
	}
	return isAvailable
}
