package features

import (
	"github.com/golang/glog"
)

// Flags feature flags
var Flags *Features

func init() {
	Flags = &Features{}
}

// Features ...
type Features struct {
	oc          bool
	bc          bool
	logSeverity bool
	gm          bool
	gmHoldover  bool
	syncE       bool
	dualBC      bool
	twoPortOC   bool
	ha          bool
	leapFile    bool
	bcHoldover  bool
}

// Versions of linuxptp we compare too
const (
	linuxPTPVersion3112 = "3.1.1-2.el8_6.3"
	linuxPTPVersion3116 = "3.1.1-6.el9_2.7"
	linuxPTPVersion422  = "4.2-2.el9_4.3"
	linuxPTPVersion441  = "4.4-1.el9"
)

// Comparible versions the semver version we compare to
var (
	VersionLinuxPTP3112 = mustGetSemver(linuxPTPVersion3112)
	VersionLinuxPTP3116 = mustGetSemver(linuxPTPVersion3116)
	VersionLinuxPTP422  = mustGetSemver(linuxPTPVersion422)
	VersionLinuxPTP441  = mustGetSemver(linuxPTPVersion441)
)

// Init ...
func (f *Features) Init(versionStr string) {
	version, err := getSemver(versionStr)
	if err != nil {
		glog.Fatalf("Failed to parse ptp version '%s'", versionStr)
	}

	// Check if version is >= 3.1.1-2.el8_6.3
	if version.Compare(VersionLinuxPTP3112) >= 0 {
		f.oc = true
		f.bc = true
		f.dualBC = true
	}

	// Check if version >= 3.1.1-6.el9_2.7
	if version.Compare(VersionLinuxPTP3116) >= 0 {
		f.gm = true
		f.leapFile = true
	}

	// Check if version >= 4.2-2.el9_4.3
	if version.Compare(VersionLinuxPTP422) >= 0 {
		f.logSeverity = true
		f.gmHoldover = true
		f.syncE = true
		f.ha = true
	}

	// Check if version >= 4.4-1.el9
	if version.Compare(VersionLinuxPTP441) >= 0 {
		f.twoPortOC = true
		f.bcHoldover = true
	}
}

// Print prints out the internal values of feature gflags
func (f *Features) Print() {
	glog.Infof("OC         : %v", f.oc)
	glog.Infof("BC         : %v", f.bc)
	glog.Infof("DualBC     : %v", f.dualBC)
	glog.Infof("GM         : %v", f.gm)
	glog.Infof("LeapFile   : %v", f.leapFile)
	glog.Infof("LogSeverity: %v", f.logSeverity)
	glog.Infof("GMHoldover : %v", f.gmHoldover)
	glog.Infof("SyncE      : %v", f.syncE)
	glog.Infof("HA         : %v", f.ha)
	glog.Infof("BCHoldover : %v", f.bcHoldover)
}

// IsDualBCAvailable ...
func (f *Features) IsDualBCAvailable() bool {
	return f.dualBC
}

// IsGMAvailable ...
func (f *Features) IsGMAvailable() bool {
	return f.gm
}

// IsLeapFileAvailable ...
func (f *Features) IsLeapFileAvailable() bool {
	return f.leapFile
}

// IsLogSeverityAvailable ...
func (f *Features) IsLogSeverityAvailable() bool {
	return f.logSeverity
}

// IsGMHoldoverAvailable ...
func (f *Features) IsGMHoldoverAvailable() bool {
	return f.gmHoldover
}

// IsSyncEAvailable ...
func (f *Features) IsSyncEAvailable() bool {
	return f.syncE
}

// IsHAAvailable ...
func (f *Features) IsHAAvailable() bool {
	return f.ha
}

// IsBCHoldoverAvailable ...
func (f *Features) IsBCHoldoverAvailable() bool {
	return f.bcHoldover
}

// =========================================================== //
// NOTE:                                                       //
// This is an alternative API for checking feature flags       //
// We should pick one but I wanted to leave it to the person   //
// Implementing the first feature to decide which API to use   //
// Once they have decied we should remove the other one        //
// =========================================================== //

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

// IsFeatureAvailable takes a feature flag returns if its enabled
func (f *Features) IsFeatureAvailable(feature FeatureFlag) bool {
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

// AreFeaturesAvailable take one or more feature flags and returns if they are all enabled
func (f *Features) AreFeaturesAvailable(features ...FeatureFlag) bool {
	isAvailable := true
	for _, flag := range features {
		isAvailable = isAvailable && f.IsFeatureAvailable(flag)
	}
	return isAvailable
}
