package features

import (
	"os/exec"
	"strings"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/features/version"
)

// Flags feature flags
var Flags *Features
var getVersion func() string

func init() {
	Flags = &Features{}
	getVersion = getLinuxPTPPackageVersion
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

// Versions ...
const (
	linuxPTPVersion3112 = "3.1.1-2.el8_6.3"
	linuxPTPVersion3116 = "3.1.1-6.el9_2.7"
	linuxPTPVersion422  = "4.2-2.el9_4.3"
	linuxPTPVersion441  = "4.4-1.el9"
)

// Comparible versions ...
var (
	VersionLinuxPTP3112 = version.MustParse(linuxPTPVersion3112)
	VersionLinuxPTP3116 = version.MustParse(linuxPTPVersion3116)
	VersionLinuxPTP422  = version.MustParse(linuxPTPVersion422)
	VersionLinuxPTP441  = version.MustParse(linuxPTPVersion441)
)

func runCmd(cmdLine string) string {
	args := strings.Fields(cmdLine)
	cmd := exec.Command(args[0], args[1:]...)
	outBytes, _ := cmd.CombinedOutput()
	return string(outBytes)
}

func getLinuxPTPPackageVersion() string {
	out := runCmd("rpm -q linuxptp")
	glog.Infof("linuxptp package version is: %s", out)
	version, _ := strings.CutPrefix(out, "linuxptp-")
	version, _ = strings.CutSuffix(version, "."+runCmd("arch"))
	return version
}

// Init ...
func (f *Features) Init() {
	versionStr := getVersion()
	version := version.MustParse(versionStr)

	if VersionLinuxPTP3112.Compare(version) >= 0 {
		f.oc = true
		f.bc = true
		f.dualBC = true
	}

	if VersionLinuxPTP3116.Compare(version) >= 0 {
		f.gm = true
		f.leapFile = true
	}

	if VersionLinuxPTP422.Compare(version) >= 0 {
		f.logSeverity = true
		f.gmHoldover = true
		f.syncE = true
		f.ha = true
	}

	if VersionLinuxPTP441.Compare(version) >= 0 {
		f.twoPortOC = true
		f.bcHoldover = true
	}
}

// IsDualBCAvaiable ...
func (f *Features) IsDualBCAvaiable() bool {
	return f.dualBC
}

// IsGMAvaiable ...
func (f *Features) IsGMAvaiable() bool {
	return f.gm
}

// IsLeapFileAvaiable ...
func (f *Features) IsLeapFileAvaiable() bool {
	return f.leapFile
}

// IsLogSeverityAvaiable ...
func (f *Features) IsLogSeverityAvaiable() bool {
	return f.logSeverity
}

// IsGMHoldoverAvaiable ...
func (f *Features) IsGMHoldoverAvaiable() bool {
	return f.gmHoldover
}

// IsSyncEAvaiable ...
func (f *Features) IsSyncEAvaiable() bool {
	return f.syncE
}

// IsHAAvaiable ...
func (f *Features) IsHAAvaiable() bool {
	return f.ha
}

// IsBCHoldoverAvaiable ...
func (f *Features) IsBCHoldoverAvaiable() bool {
	return f.bcHoldover
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

// SetVersionForTesting ... used by unit tests
func SetVersionForTesting(version string) {
	getVersion = func() string {
		return version
	}
}
