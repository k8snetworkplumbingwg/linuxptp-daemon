package daemon

// This tests daemon private functions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bigkevmcd/go-configparser"
	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/alias"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	dpll "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/hardwareconfig"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/network"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/synce"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/testhelpers"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/yaml"
)

const (
	testDUTLeadingIface = "ens2f0"
	labelIface          = "iface"
	labelNode           = "node"
	labelProcess        = "process"
)

// vendor defaults are embedded; no filesystem setup needed

// NewProcessManager is used by unit tests
func NewProcessManager() *ProcessManager {
	processPTP := &ptpProcess{}
	processPTP.ptpClockThreshold = &ptpv1.PtpClockThreshold{
		HoldOverTimeout:    5,
		MaxOffsetThreshold: 100,
		MinOffsetThreshold: -100,
	}
	return &ProcessManager{
		process: []process.Process{processPTP},
	}
}

// SetTestProfileProcess ...
func (p *ProcessManager) SetTestProfileProcess(name string, ifaces config.IFaces,
	socketPath, configPath string, nodeProfile ptpv1.PtpProfile,
) {
	p.process = append(p.process, &ptpProcess{
		ExecProcess: ExecProcess{
			name:       name,
			configPath: configPath,
		},
		socketPath: socketPath,
		ifaces:     ifaces,

		nodeProfile: &nodeProfile,
	})
}

func findPtpProc(p *ProcessManager) *ptpProcess {
	for _, proc := range p.process {
		if ptpProc, ok := proc.(*ptpProcess); ok {
			return ptpProc
		}
	}
	return nil
}

// SetTestData is used by unit tests
func (p *ProcessManager) SetTestData(name, msgTag string, ifaces config.IFaces) {
	if len(p.process) < 1 || p.process[0] == nil {
		glog.Error("process is not initialized in SetTestData()")
		return
	}
	eventChannel := make(chan event.Event)
	ptpProc := findPtpProc(p)
	if ptpProc == nil {
		glog.Error("ptp process found in in SetTestData()")
		return
	}
	ptpProc.name = name
	ptpProc.messageTag = msgTag
	ptpProc.ifaces = ifaces
	ptpProc.logParser = getParser(name)
	ptpProc.eventCh = eventChannel
	// Reset aliases for each test to avoid cross-case collisions.
	alias.ClearAliases()
	// Calculate aliases for the test interfaces to ensure proper aliasing
	for phc, ifNames := range ifaces.GetIfNamesGroupedByPhc() {
		for _, ifname := range ifNames {
			alias.AddInterface(phc, ifname)
		}
	}
	alias.CalculateAliases()
}

// RunProcessPTPMetrics is used by unit tests
func (p *ProcessManager) RunProcessPTPMetrics(log string) {
	if len(p.process) < 1 || p.process[0] == nil {
		glog.Error("process is not initialized in RunProcessPTPMetrics()")
		return
	}
	ptpProc := findPtpProc(p)
	if ptpProc == nil {
		glog.Error("ptp process found in RunProcessPTPMetrics()")
		return
	}

	ptpProc.processPTPMetrics(log)
}

// RunSynceParser is used by unit tests
func (p *ProcessManager) RunSynceParser(log string) {
	if len(p.process) < 1 || p.process[0] == nil {
		glog.Error("process is not initialized in RunSynceParser()")
		return
	}
	logEntry := synce.ParseLog(log)
	ptpProc := findPtpProc(p)
	if ptpProc == nil {
		glog.Error("ptp process found in RunSynceParser()")
		return
	}
	ptpProc.processSynceEvents(logEntry)
}

// UpdateSynceConfig is used by unit tests
func (p *ProcessManager) UpdateSynceConfig(config *synce.Relations) {
	if len(p.process) == 0 {
		glog.Error("process is not initialized in UpdateSynceConfig()")
		return
	}
	ptpProc := findPtpProc(p)
	if ptpProc == nil {
		glog.Error("ptp process found in UpdateSynceConfig()")
		return
	}
	ptpProc.syncERelations = config
}

// NewDaemonForTests creates a Daemon instance for testing
func NewDaemonForTests(tracker *ReadyTracker, processManager *ProcessManager) *Daemon {
	tracker.processManager = processManager
	fakeClient := fake.NewClientset()
	ctx, cancel := context.WithCancel(context.Background())
	return &Daemon{
		readyTracker:          tracker,
		processManager:        processManager,
		hardwareConfigManager: hardwareconfig.NewHardwareConfigManager(fakeClient, "default", nil),
		ctx:                   ctx,
		cancel:                cancel,
	}
}

func loadProfile(path string) (*ptpv1.PtpProfile, error) {
	profileData, err := os.ReadFile(path)
	if err != nil {
		return &ptpv1.PtpProfile{}, err
	}
	profile := ptpv1.PtpProfile{}
	err = yaml.Unmarshal(profileData, &profile)
	if err != nil {
		return &ptpv1.PtpProfile{}, err
	}
	return &profile, nil
}

func mkPath(t *testing.T) {
	err := os.MkdirAll("/tmp/test", os.ModePerm)
	assert.NoError(t, err)
}

// --- Local JSON→Pin loader for tests (to avoid relying on hardwareconfig internals) ---
type hrPin struct {
	ID           uint32        `json:"id"`
	ModuleName   string        `json:"moduleName"`
	ClockID      string        `json:"clockId"`
	BoardLabel   string        `json:"boardLabel"`
	Type         string        `json:"type"`
	Frequency    uint64        `json:"frequency"`
	ParentDevice []hrParentDev `json:"pinParentDevice"`
}

type hrParentDev struct {
	ParentID  uint32  `json:"parentID"`
	Direction string  `json:"direction"`
	Prio      *uint32 `json:"prio,omitempty"`
	State     string  `json:"state"`
}

// ParseClockIDHex parses a hex clock ID string (e.g., "0x507c6f...") into uint64.
func parseClockIDHex(s string) uint64 {
	s = strings.TrimPrefix(s, "0x")
	v, _ := strconv.ParseUint(s, 16, 64)
	return v
}

func createMockDpllPinsGetterFromFile(path string) (hardwareconfig.DpllPinsGetter, error) { //nolint:unparam
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var hrs []hrPin
	if unmarshalErr := json.Unmarshal(data, &hrs); unmarshalErr != nil {
		return nil, unmarshalErr
	}
	var pins []*dpll.PinInfo
	for _, h := range hrs {
		p := &dpll.PinInfo{
			ID:           h.ID,
			ModuleName:   h.ModuleName,
			ClockID:      parseClockIDHex(h.ClockID),
			BoardLabel:   h.BoardLabel,
			Type:         dpll.ParsePinType(h.Type),
			Frequency:    h.Frequency,
			Capabilities: 0,
		}
		for _, pd := range h.ParentDevice {
			p.ParentDevice = append(p.ParentDevice, dpll.PinParentDevice{
				ParentID:  pd.ParentID,
				Direction: dpll.ParsePinDirection(pd.Direction),
				Prio:      pd.Prio,
				State:     dpll.ParsePinState(pd.State),
			})
		}
		pins = append(pins, p)
	}
	return hardwareconfig.CreateMockDpllPinsGetter(pins, nil), nil
}

func clean(t *testing.T) {
	err := os.RemoveAll("/tmp/test")
	assert.NoError(t, err)
}

func applyTestProfile(t *testing.T, profile *ptpv1.PtpProfile) {
	stopCh := make(<-chan struct{})
	assert.NoError(t, leap.MockLeapFile())
	defer func() {
		leap.GetManager().Stop()
		assert.Nil(t, leap.GetManager())
	}()
	dn := New(
		"test-node-name",
		"openshift-ptp",
		nil,
		nil,
		&LinuxPTPConfUpdate{
			UpdateCh:     make(chan bool),
			NodeProfiles: []ptpv1.PtpProfile{*profile},
		},
		stopCh,
		[]string{"e810"},
		&[]ptpv1.HwConfig{},
		nil,
		make(chan bool),
		30,
		&ReadyTracker{},
	)
	assert.NotNil(t, dn)
	// Signal that no hardware configs are expected for this test
	_ = dn.hardwareConfigManager.UpdateHardwareConfig([]ptpv2alpha1.HardwareConfig{})
	topology, err := NewProfileTopology([]ptpv1.PtpProfile{*profile})
	require.NoError(t, err)
	err = dn.applyNodePtpProfile(0, profile, &topology)
	assert.NoError(t, err)
}

func testRequirements(t *testing.T, profile *ptpv1.PtpProfile) {
	cfg, err := configparser.NewConfigParserFromFile("/tmp/test/synce4l.0.config")
	assert.NoError(t, err)
	for _, sec := range cfg.Sections() {
		if strings.HasPrefix(sec, "[<") {
			clk, err := cfg.Get(sec, "clock_id")
			assert.NoError(t, err)
			id, found := profile.PtpSettings["test_clock_id_override"]
			if found {
				assert.NotEqual(t, id, clk)
			} else {
				assert.NotEqual(t, "0", clk)
				assert.NotEqual(t, "", clk)
			}
		}
	}
}

func Test_applyProfile_synce(t *testing.T) {
	defer clean(t)
	testDataFiles := []string{
		"testdata/synce-profile.yaml",
		"testdata/synce-profile-dual.yaml",
		"testdata/synce-profile-custom-id.yaml",
		"testdata/synce-profile-bad-order.yaml",
		"testdata/synce-profile-no-ifaces.yaml",
		"testdata/synce-follower-profile.yaml",
	}
	for i := range len(testDataFiles) {
		mkPath(t)
		profile, err := loadProfile(testDataFiles[i])
		assert.NoError(t, err)
		applyTestProfile(t, profile)
		testRequirements(t, profile)
		clean(t)
	}
}

func Test_applyProfile_TBC(t *testing.T) {
	defer clean(t)

	// Set up mock DPLL pins for testing (load from hardwareconfig testdata)
	if getter, err := createMockDpllPinsGetterFromFile("../hardwareconfig/testdata/pins.json"); err == nil {
		hardwareconfig.SetDpllPinsGetter(getter)
	} else {
		t.Logf("Warning: Failed to setup mock DPLL pins from file: %v", err)
		// Continue with test as DPLL pins are optional
	}
	defer hardwareconfig.TeardownMockDpllPinsForTests()

	tests := []struct {
		dataFile          string
		expectedProcesses []string
	}{
		{
			dataFile:          "testdata/profile-tbc-tt.yaml",
			expectedProcesses: []string{event.Ptp4lProcessName},
		},
		{
			dataFile:          "testdata/profile-tbc-tr.yaml",
			expectedProcesses: []string{event.Ptp4lProcessName, event.Ptp4lProcessName, event.Ts2PhcProcessName, event.Phc2SysProcessName},
		},
	}
	stopCh := make(<-chan struct{})
	assert.NoError(t, leap.MockLeapFile())
	defer func() {
		leap.GetManager().Stop()
		assert.Nil(t, leap.GetManager())
	}()
	dn := New(
		"test-node-name",
		"openshift-ptp",
		nil,
		nil,
		&LinuxPTPConfUpdate{
			UpdateCh:     make(chan bool),
			NodeProfiles: []ptpv1.PtpProfile{},
		},
		stopCh,
		[]string{"e810"},
		&[]ptpv1.HwConfig{},
		nil,
		make(chan bool),
		30,
		&ReadyTracker{},
	)
	assert.NotNil(t, dn)
	// Signal that no hardware configs are expected for this test
	_ = dn.hardwareConfigManager.UpdateHardwareConfig([]ptpv2alpha1.HardwareConfig{})

	profiles := make([]ptpv1.PtpProfile, len(tests))
	for i, test := range tests {
		profile, err := loadProfile(test.dataFile)
		require.NoError(t, err)
		profiles[i] = *profile
	}
	topology, err := NewProfileTopology(profiles)
	require.NoError(t, err)
	assert.Equal(t, *profiles[0].Name, topology.controlledBy(&profiles[1]))
	assert.Equal(t, *profiles[1].Name, topology.controlling(&profiles[0]))

	for runID, test := range tests {
		mkPath(t)
		err = dn.applyNodePtpProfile(runID, &profiles[runID], &topology)
		assert.NoError(t, err)
		if runID == 0 {
			assert.Nil(t, dn.processManager.clockMgr.GetClock(clockCfgName(runID)), "controlled profile must not register a separate clock")
		} else {
			controllerClock := dn.processManager.clockMgr.GetClock(clockCfgName(runID))
			require.NotNil(t, controllerClock)
			assert.Equal(t, event.TBC, controllerClock.ClockType())
		}

		// Ensure for T-BC that phc2sys has a non-Immediate start condition (delayed start)
		actualProcesses := []string{}
		for _, p := range dn.processManager.process {
			actualProcesses = append(actualProcesses, p.Name())
			if p.Name() == event.Phc2SysProcessName {
				cond := process.GetCondition(p, process.ActionStart, process.Immediate{})
				sourceCond, ok := cond.(process.OnSourceQualified)
				assert.True(t, ok, "T-BC phc2sys must wait on source qualification, got %s", cond)
				assert.True(t, strings.HasPrefix(sourceCond.ConfigName, "ptp4l."), sourceCond.ConfigName)
			}
			if p.Name() == event.Ts2PhcProcessName {
				cond := process.GetCondition(p, process.ActionStart, process.Immediate{})
				assert.NotEqual(t, process.Immediate{}, cond, "Ensure ts2phc is startup-delayed for T-BC")
			}
		}
		assert.ElementsMatch(t, test.expectedProcesses, actualProcesses, "Ensure T-BC has the required processes prepared (%s)", test.dataFile)
		clean(t)
	}
}

func Test_applyProfile_DualNICBCHAClockType(t *testing.T) {
	ct := NewCondiitonsTester(t, event.BC)
	configDir := configPrefix

	bc1Conf := "[global]\n[ens1f0]\nmasterOnly 0\n[ens1f1]\nmasterOnly 1\n"
	bc2Conf := "[global]\n[ens2f0]\nmasterOnly 0\n[ens2f1]\nmasterOnly 1\n"
	bcOpts := "-m"
	noPtp4l := ""
	phc2sysOpts := "-a -r -m -l 7 -n 24 "
	// CI sets phc2sysOpts but leaves phc2sysConf unset on the HA profile.
	profiles := []ptpv1.PtpProfile{
		{Name: stringPointer("bc1"), Ptp4lConf: &bc1Conf, Ptp4lOpts: &bcOpts, PtpSettings: map[string]string{"unitTest": configDir}},
		{Name: stringPointer("bc2"), Ptp4lConf: &bc2Conf, Ptp4lOpts: &bcOpts, PtpSettings: map[string]string{"unitTest": configDir}},
		{Name: stringPointer("ha"), Ptp4lOpts: &noPtp4l, Phc2sysOpts: &phc2sysOpts,
			PtpSettings: map[string]string{"unitTest": configDir, PTP_HA_IDENTIFIER: "bc1,bc2"}},
	}

	dn := NewDaemonForTests(&ReadyTracker{}, ct.pm)
	t.Cleanup(dn.cancel)
	dn.ptpUpdate = &LinuxPTPConfUpdate{NodeProfiles: profiles}
	topology, err := NewProfileTopology(profiles)
	require.NoError(t, err)

	require.NoError(t, dn.assembleHAClock("ha", []string{"bc1", "bc2"}, &topology))
	dn.populateHAInterfaces(&topology)

	clockTypes := map[string]event.ClockType{}
	for _, proc := range dn.processManager.process {
		if ptpProc, ok := proc.(*ptpProcess); ok && ptpProc.nodeProfile != nil {
			clockTypes[*ptpProc.nodeProfile.Name] = ptpProc.clockType
		}
	}
	assert.Equal(t, event.BC, clockTypes["bc1"])
	assert.Equal(t, event.BC, clockTypes["bc2"])
	assert.Equal(t, event.SysClock, clockTypes["ha"], "CI omits Phc2sysConf; HA profile must still be detected")

	// The composite HA clock must own both members: it is registered under the HA
	// phc2sys config and routes each member's ptp4l config to the same clock.
	haCfg := fmt.Sprintf("phc2sys.%d.config", topology.profileRunID["ha"])
	haClk := dn.processManager.clockMgr.GetClock(haCfg)
	require.NotNil(t, haClk, "HA clock should be registered under %s", haCfg)
	assert.Equal(t, event.HA, haClk.ClockType())
	for _, member := range []string{"bc1", "bc2"} {
		memberCfg := fmt.Sprintf("ptp4l.%d.config", topology.profileRunID[member])
		assert.Contains(t, haClk.ConfigNames(), memberCfg, "HA clock should compose member %s", member)
		assert.Same(t, haClk, dn.processManager.clockMgr.GetClock(memberCfg), "member %s events should route to the HA clock", member)
	}

	for _, proc := range dn.processManager.process {
		if ptpProc, ok := proc.(*ptpProcess); ok && ptpProc.Name() == event.Phc2SysProcessName {
			assert.Equal(t, string(event.SysClock), reportedClockType(ptpProc))
			assert.Equal(t, process.Immediate{}, process.GetCondition(ptpProc, process.ActionStart, process.Immediate{}))
			assert.ElementsMatch(t, []string{"ens1f0", "ens1f1"}, ptpProc.haProfile["bc1"])
			assert.ElementsMatch(t, []string{"ens2f0", "ens2f1"}, ptpProc.haProfile["bc2"])
			t.Logf("HA phc2sys inferred clock type: %s; NodePtpDevice reports: %s", ptpProc.clockType, reportedClockType(ptpProc))
		}
	}
}

func Test_applyProfile_TGM(t *testing.T) {
	defer clean(t)
	mkPath(t)

	stopCh := make(<-chan struct{})
	assert.NoError(t, leap.MockLeapFile())
	defer func() {
		leap.GetManager().Stop()
		assert.Nil(t, leap.GetManager())
	}()
	dn := New(
		"test-node-name",
		"openshift-ptp",
		nil,
		nil,
		&LinuxPTPConfUpdate{
			UpdateCh:     make(chan bool),
			NodeProfiles: []ptpv1.PtpProfile{},
		},
		stopCh,
		[]string{"e810"},
		&[]ptpv1.HwConfig{},
		nil,
		make(chan bool),
		30,
		&ReadyTracker{},
	)
	assert.NotNil(t, dn)
	_ = dn.hardwareConfigManager.UpdateHardwareConfig([]ptpv2alpha1.HardwareConfig{})

	profile, err := loadProfile("testdata/profile-tgm.yaml")
	assert.NoError(t, err)
	topology, err := NewProfileTopology([]ptpv1.PtpProfile{*profile})
	require.NoError(t, err)
	err = dn.applyNodePtpProfile(0, profile, &topology)
	assert.NoError(t, err)

	var ts2phcProc *ptpProcess
	var ptp4lProc *ptpProcess
	for _, p := range dn.processManager.process {
		ptp, ok := p.(*ptpProcess)
		if !ok {
			continue
		}
		switch ptp.Name() {
		case event.Ts2PhcProcessName:
			ts2phcProc = ptp
			cond := process.GetCondition(ptp, process.ActionStart, process.Immediate{})
			_, isTBCDelay := cond.(process.OnPluginEvent)
			assert.False(t, isTBCDelay, "T-GM ts2phc must not have a delayed start condition")
		case event.Ptp4lProcessName:
			ptp4lProc = ptp
		case event.Phc2SysProcessName:
			cond := process.GetCondition(ptp, process.ActionStart, process.Immediate{})
			var offsetCond process.OnStateAndOffsetForCount
			offsetCond, isOffsetCond := cond.(process.OnStateAndOffsetForCount)
			assert.True(t, isOffsetCond, "T-GM phc2sys must wait on ts2phc lock/offset, got %s", cond)
			assert.Equal(t, event.TS2PHC, offsetCond.Source)
			assert.Equal(t, "ts2phc.0.config", offsetCond.ConfigName)
		}
	}

	// 1. ts2phc must have both gpsd and gpspipe as dependent processes.
	if assert.NotNil(t, ts2phcProc, "ts2phc process should exist for T-GM profile") {
		depNames := make([]string, len(ts2phcProc.depProcess))
		for i, d := range ts2phcProc.depProcess {
			depNames[i] = d.Name()
		}
		assert.Contains(t, depNames, event.GpsdProcessName, "gpsd must be a dependent process of ts2phc for T-GM")
		assert.Contains(t, depNames, event.GpsPipeProcessName, "gpspipe must be a dependent process of ts2phc for T-GM")

		for _, d := range ts2phcProc.depProcess {
			if gpsd, ok := d.(*GPSD); ok {
				assert.Equal(t, "ens7f0", gpsd.gmInterface,
					"GPSD gmInterface should match the first interface in ts2phcConf (leading GNSS-sourced interface)")
			}
		}
	}

	// 2. ptp4l should NOT have a PMC dependent process for GM profiles.
	if assert.NotNil(t, ptp4lProc, "ptp4l process should exist for T-GM profile") {
		for _, d := range ptp4lProc.depProcess {
			assert.NotEqual(t, "pmc", d.Name(), "ptp4l should not have PMC as a dependent process for T-GM")
		}
	}

	// 3. ts2phc opts must include holdover and servo parameters for GM.
	// These are auto-appended by applyNodePtpProfile for GM clock types.
	ts2phcOpts := *profile.Ts2PhcOpts
	assert.Contains(t, ts2phcOpts, "--ts2phc.holdover", "ts2phc opts must include holdover timeout for T-GM")
	assert.Contains(t, ts2phcOpts, "--servo_offset_threshold", "ts2phc opts must include servo offset threshold for T-GM")
	assert.Contains(t, ts2phcOpts, "--servo_num_offset_values 10", "ts2phc opts must include servo num offset values for T-GM")
}

func TestGetPTPClockId_ValidInput(t *testing.T) {
	p := &ptpProcess{
		nodeProfile: &ptpv1.PtpProfile{
			PtpSettings: map[string]string{
				"leadingInterface": "eth0",
				"clockId[eth0]":    "123456",
			},
		},
	}

	expectedClockID := "000000.fffe.01e240"
	actualClockID, err := p.getPTPClockID()
	assert.NoError(t, err)
	assert.Equal(t, expectedClockID, actualClockID)
}

func TestGetPTPClockId_MissingLeadingInterface(t *testing.T) {
	p := &ptpProcess{
		nodeProfile: &ptpv1.PtpProfile{
			PtpSettings: map[string]string{},
		},
	}

	_, err := p.getPTPClockID()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "leadingInterface not found in ptpProfile")
}

func TestGetPTPClockId_MissingClockId(t *testing.T) {
	p := &ptpProcess{
		nodeProfile: &ptpv1.PtpProfile{
			PtpSettings: map[string]string{
				"leadingInterface": "eth0",
			},
		},
	}

	_, err := p.getPTPClockID()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "leading interface ClockId not found in ptpProfile")
}

func TestGetPTPClockId_ParsingError(t *testing.T) {
	p := &ptpProcess{
		nodeProfile: &ptpv1.PtpProfile{
			PtpSettings: map[string]string{
				"leadingInterface": "eth0",
				"clockId[eth0]":    "invalid_string",
			},
		},
	}

	_, err := p.getPTPClockID()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse clock ID string invalid_string")
}

func TestReconcileRelatedProfiles(t *testing.T) {
	tests := []struct {
		name           string
		profiles       []ptpv1.PtpProfile
		expectedResult map[string]int
		description    string
	}{
		{
			name:           "empty profiles",
			profiles:       []ptpv1.PtpProfile{},
			expectedResult: map[string]int{},
			description:    "should return empty map when no profiles provided",
		},
		{
			name: "no controlling profiles",
			profiles: []ptpv1.PtpProfile{
				{
					Name:        stringPointer("profile1"),
					PtpSettings: map[string]string{},
				},
				{
					Name:        stringPointer("profile2"),
					PtpSettings: map[string]string{},
				},
			},
			expectedResult: map[string]int{},
			description:    "should return empty map when no profiles have controllingProfile setting",
		},
		{
			name: "single controlling profile relationship",
			profiles: []ptpv1.PtpProfile{
				{
					Name:        stringPointer("controller"),
					PtpSettings: map[string]string{},
				},
				{
					Name: stringPointer("controlled"),
					PtpSettings: map[string]string{
						"controllingProfile": "controller",
					},
				},
			},
			expectedResult: map[string]int{
				"controller": 1, // controlled profile is at index 1
			},
			description: "should map controlling profile to controlled profile's index",
		},
		{
			name: "multiple controlling profile relationships",
			profiles: []ptpv1.PtpProfile{
				{
					Name:        stringPointer("controller1"),
					PtpSettings: map[string]string{},
				},
				{
					Name: stringPointer("controlled1"),
					PtpSettings: map[string]string{
						"controllingProfile": "controller1",
					},
				},
				{
					Name:        stringPointer("controller2"),
					PtpSettings: map[string]string{},
				},
				{
					Name: stringPointer("controlled2"),
					PtpSettings: map[string]string{
						"controllingProfile": "controller2",
					},
				},
			},
			expectedResult: map[string]int{
				"controller1": 1, // controlled1 is at index 1
				"controller2": 3, // controlled2 is at index 3
			},
			description: "should handle multiple controlling/controlled relationships",
		},
		{
			name: "controlling profile not found",
			profiles: []ptpv1.PtpProfile{
				{
					Name: stringPointer("controlled"),
					PtpSettings: map[string]string{
						"controllingProfile": "nonexistent",
					},
				},
			},
			expectedResult: map[string]int{},
			description:    "should return empty map when controlling profile doesn't exist",
		},
		{
			name: "controlled profile references nonexistent controller",
			profiles: []ptpv1.PtpProfile{
				{
					Name:        stringPointer("profile1"),
					PtpSettings: map[string]string{},
				},
				{
					Name: stringPointer("profile2"),
					PtpSettings: map[string]string{
						"controllingProfile": "nonexistent_controller",
					},
				},
			},
			expectedResult: map[string]int{},
			description:    "should handle case where controlled profile references non-existent controller",
		},
		{
			name: "empty controllingProfile value",
			profiles: []ptpv1.PtpProfile{
				{
					Name:        stringPointer("controller"),
					PtpSettings: map[string]string{},
				},
				{
					Name: stringPointer("controlled"),
					PtpSettings: map[string]string{
						"controllingProfile": "",
					},
				},
			},
			expectedResult: map[string]int{},
			description:    "should ignore profiles with empty controllingProfile value",
		},
		{
			name: "complex scenario with mixed relationships",
			profiles: []ptpv1.PtpProfile{
				{
					Name:        stringPointer("standalone"),
					PtpSettings: map[string]string{},
				},
				{
					Name:        stringPointer("controller1"),
					PtpSettings: map[string]string{},
				},
				{
					Name: stringPointer("controlled1"),
					PtpSettings: map[string]string{
						"controllingProfile": "controller1",
					},
				},
				{
					Name: stringPointer("controlled_orphan"),
					PtpSettings: map[string]string{
						"controllingProfile": "missing_controller",
					},
				},
				{
					Name:        stringPointer("controller2"),
					PtpSettings: map[string]string{},
				},
				{
					Name: stringPointer("controlled2"),
					PtpSettings: map[string]string{
						"controllingProfile": "controller2",
					},
				},
			},
			expectedResult: map[string]int{
				"controller1": 2, // controlled1 is at index 2
				"controller2": 5, // controlled2 is at index 5
			},
			description: "should handle complex scenario with standalone, valid relationships, and orphaned controlled profiles",
		},
		{
			name: "same controller for multiple controlled profiles (only last one should be recorded)",
			profiles: []ptpv1.PtpProfile{
				{
					Name:        stringPointer("controller"),
					PtpSettings: map[string]string{},
				},
				{
					Name: stringPointer("controlled1"),
					PtpSettings: map[string]string{
						"controllingProfile": "controller",
					},
				},
				{
					Name: stringPointer("controlled2"),
					PtpSettings: map[string]string{
						"controllingProfile": "controller",
					},
				},
			},
			expectedResult: map[string]int{
				"controller": 2, // controlled2 is at index 2 (overwrites controlled1)
			},
			description: "should handle case where multiple profiles reference same controller (last one wins)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconcileRelatedProfiles(tt.profiles)
			assert.Equal(t, tt.expectedResult, result, tt.description)
		})
	}
}

// Helper function to create string pointers
func stringPointer(s string) *string {
	return &s
}

func TestNewProfileTopology(t *testing.T) {
	phc2sysOpts := "-a -r -m -l 7 -n 24 "

	t.Run("normal behavior without HA", func(t *testing.T) {
		profiles := []ptpv1.PtpProfile{
			{Name: stringPointer("bc1"), Phc2sysOpts: &phc2sysOpts,
				Plugins: map[string]*apiextensions.JSON{
					"ntpfailover": {Raw: []byte("{}")},
				}},
		}

		topology, err := NewProfileTopology(profiles)
		require.NoError(t, err)

		assert.Empty(t, topology.haProfile, "no HA profile expected")
		assert.Empty(t, topology.haMembers)
		assert.Equal(t, map[string]int{"bc1": 0}, topology.profileRunID)
		assert.ElementsMatch(t, []string{"bc1"}, topology.phc2sysProfiles)
		assert.ElementsMatch(t, []string{"bc1"}, topology.ntpFailOverProfileNames,
			"profile with ntpfailover plugin should be tracked")
	})
	t.Run("empty HA list is standalone", func(t *testing.T) {
		for _, members := range []string{"", "  "} {
			profile := ptpv1.PtpProfile{Name: stringPointer("tr"), Phc2sysOpts: &phc2sysOpts,
				PtpSettings: map[string]string{PTP_HA_IDENTIFIER: members}}
			topology, err := NewProfileTopology([]ptpv1.PtpProfile{profile})
			require.NoError(t, err)
			assert.False(t, topology.isHAProfile("tr"))
			assert.Empty(t, topology.haMembers)
		}
	})

	t.Run("valid HA profile with members", func(t *testing.T) {
		profiles := []ptpv1.PtpProfile{
			{Name: stringPointer("bc1")},
			{Name: stringPointer("bc2")},
			{Name: stringPointer("ha"), Phc2sysOpts: &phc2sysOpts,
				PtpSettings: map[string]string{PTP_HA_IDENTIFIER: "bc1,bc2"}},
		}

		topology, err := NewProfileTopology(profiles)
		require.NoError(t, err)

		assert.Equal(t, "ha", topology.haProfile)
		assert.ElementsMatch(t, []string{"bc1", "bc2"}, topology.haMembers)
		assert.Equal(t, 2, topology.profileRunID["ha"])
		assert.True(t, topology.isHAProfile("ha"))
		assert.True(t, topology.isHAMember("bc1"))
		assert.True(t, topology.isHAMember("bc2"))
	})
	t.Run("TBC cannot configure HA", func(t *testing.T) {
		for _, opts := range []*string{nil, &phc2sysOpts} {
			profile := ptpv1.PtpProfile{Name: stringPointer("tr"), Phc2sysOpts: opts,
				PtpSettings: map[string]string{"clockType": TBC, PTP_HA_IDENTIFIER: "bc1"}}
			_, err := NewProfileTopology([]ptpv1.PtpProfile{profile})
			require.ErrorContains(t, err, "T-BC")
			require.ErrorContains(t, err, "HA")
		}
	})
	t.Run("TBC cannot be an HA member", func(t *testing.T) {
		profiles := []ptpv1.PtpProfile{
			{Name: stringPointer("tr"), PtpSettings: map[string]string{"clockType": TBC}},
			{Name: stringPointer("ha"), Phc2sysOpts: &phc2sysOpts, PtpSettings: map[string]string{PTP_HA_IDENTIFIER: "tr"}},
		}
		_, err := NewProfileTopology(profiles)
		require.ErrorContains(t, err, "T-BC")
		require.ErrorContains(t, err, "HA")
	})
	t.Run("HA profile referencing a missing member fails", func(t *testing.T) {
		profiles := []ptpv1.PtpProfile{
			{Name: stringPointer("bc1")},
			{Name: stringPointer("ha"), Phc2sysOpts: &phc2sysOpts,
				PtpSettings: map[string]string{PTP_HA_IDENTIFIER: "bc1,bc2"}},
		}

		_, err := NewProfileTopology(profiles)
		require.Error(t, err)
	})
	t.Run("HA profile with empty member fails", func(t *testing.T) {
		for _, members := range []string{"bc1,", " , , "} {
			profiles := []ptpv1.PtpProfile{
				{Name: stringPointer("bc1")},
				{Name: stringPointer("ha"), Phc2sysOpts: &phc2sysOpts,
					PtpSettings: map[string]string{PTP_HA_IDENTIFIER: members}},
			}
			_, err := NewProfileTopology(profiles)
			require.ErrorContains(t, err, "missing the following referenced profiles")
		}
	})
	t.Run("Multiple HA profiles fails", func(t *testing.T) {
		profiles := []ptpv1.PtpProfile{
			{Name: stringPointer("bc1")},
			{Name: stringPointer("bc2")},
			{Name: stringPointer("bc3")},
			{Name: stringPointer("bc4")},
			{Name: stringPointer("ha1"), Phc2sysOpts: &phc2sysOpts,
				PtpSettings: map[string]string{PTP_HA_IDENTIFIER: "bc1,bc2"}},
			{Name: stringPointer("ha2"), Phc2sysOpts: &phc2sysOpts,
				PtpSettings: map[string]string{PTP_HA_IDENTIFIER: "bc3,bc4"}},
		}

		_, err := NewProfileTopology(profiles)
		require.Error(t, err)
	})
	t.Run("missing controller fails", func(t *testing.T) {
		profiles := []ptpv1.PtpProfile{
			{Name: stringPointer("controlled"), PtpSettings: map[string]string{"controllingProfile": "missing"}},
		}
		_, err := NewProfileTopology(profiles)
		require.ErrorContains(t, err, "referenced controlling profiles")
	})
	t.Run("shared controller fails", func(t *testing.T) {
		profiles := []ptpv1.PtpProfile{
			{Name: stringPointer("controller")},
			{Name: stringPointer("first"), PtpSettings: map[string]string{"controllingProfile": "controller"}},
			{Name: stringPointer("second"), PtpSettings: map[string]string{"controllingProfile": "controller"}},
		}
		_, err := NewProfileTopology(profiles)
		require.ErrorContains(t, err, "multiple profiles being controlled by the same controller")
	})
	for _, tc := range []struct {
		name     string
		profiles []ptpv1.PtpProfile
	}{
		{"self reference", []ptpv1.PtpProfile{
			{Name: stringPointer("a"), PtpSettings: map[string]string{"controllingProfile": "a"}},
		}},
		{"two profile cycle", []ptpv1.PtpProfile{
			{Name: stringPointer("a"), PtpSettings: map[string]string{"controllingProfile": "b"}},
			{Name: stringPointer("b"), PtpSettings: map[string]string{"controllingProfile": "a"}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewProfileTopology(tc.profiles)
			require.ErrorContains(t, err, "cyclic controllingProfile")
		})
	}
	t.Run("controller chain fails", func(t *testing.T) {
		profiles := []ptpv1.PtpProfile{
			{Name: stringPointer("a")},
			{Name: stringPointer("b"), PtpSettings: map[string]string{"controllingProfile": "a"}},
			{Name: stringPointer("c"), PtpSettings: map[string]string{"controllingProfile": "b"}},
		}
		_, err := NewProfileTopology(profiles)
		require.ErrorContains(t, err, "controller chain is unsupported")
	})
}

func TestSetupGPSDAndGPSPipeFailsClosedForExplicitGNSSMatcher(t *testing.T) {
	hardwareconfig.SetDpllPinsGetter(hardwareconfig.CreateMockDpllPinsGetter(nil, nil))
	defer hardwareconfig.ResetDpllPinsGetter()

	hcm := hardwareconfig.NewHardwareConfigManager(fake.NewClientset(), "default", nil)
	hwConfig := ptpv2alpha1.HardwareConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "gnss-hwconfig"},
		Spec: ptpv2alpha1.HardwareConfigSpec{
			RelatedPtpProfileName: "test-profile",
			Profile: ptpv2alpha1.HardwareProfile{
				ClockChain: &ptpv2alpha1.ClockChain{
					Behavior: &ptpv2alpha1.Behavior{Sources: []ptpv2alpha1.SourceConfig{{
						Name:       "GNSS",
						SourceType: ptpv2alpha1.SourceTypeGNSS,
						GNSSConfig: &ptpv2alpha1.GNSSConfig{Match: &ptpv2alpha1.GNSSMatcher{}},
					}}},
				},
			},
		},
	}
	require.NoError(t, hcm.UpdateHardwareConfig([]ptpv2alpha1.HardwareConfig{hwConfig}))

	daemon := &Daemon{hardwareConfigManager: hcm}
	process := &ptpProcess{}
	err := daemon.setupGPSDAndGPSPipe(&ptpv1.PtpProfile{Name: stringPointer("tgm_test-profile")}, process)

	require.ErrorContains(t, err, "refusing to start GPSD with a fallback device")
	assert.Empty(t, process.gnssSerialPort, "failed explicit selection must not assign the default GNSS port")
	assert.Empty(t, process.depProcess, "failed explicit selection must not create GPSD or GPSPipe processes")
}

func TestTBCHardwareConfigAssociation(t *testing.T) {
	// Set up mock PTP device resolver for testing
	hardwareconfig.SetupMockPtpDeviceResolver()
	defer hardwareconfig.TeardownMockPtpDeviceResolver()

	// Set up mock DPLL pins for testing
	if getter, err := createMockDpllPinsGetterFromFile("../hardwareconfig/testdata/pins.json"); err == nil {
		hardwareconfig.SetDpllPinsGetter(getter)
	} else {
		t.Logf("Warning: Failed to setup mock DPLL pins from file: %v", err)
	}
	defer hardwareconfig.TeardownMockDpllPinsForTests()

	// Set up mock command executor for GetClockIDFromInterface
	mockCmd := hardwareconfig.NewMockCommandExecutor()
	mockCmd.SetResponse("ethtool", []string{"-i", "ens4f0"}, "driver: ice\nbus-info: 0000:17:00.0")
	mockCmd.SetResponse("lspci", []string{"-s", "0000:17:00.0"}, "17:00.0 Ethernet controller: Intel Corporation Ethernet Controller E810-C for backplane")
	mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/0000:17:00.0"}, "serial_number 50-7c-6f-ff-ff-5c-4a-e8")
	mockCmd.SetResponse("ethtool", []string{"-i", "ens8f0"}, "driver: ice\nbus-info: 0000:51:00.0")
	mockCmd.SetResponse("lspci", []string{"-s", "0000:51:00.0"}, "51:00.0 Ethernet controller: Intel Corporation Ethernet Controller E810-C for backplane")
	mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/0000:51:00.0"}, "serial_number 50-7c-6f-ff-ff-1f-b1-b8")
	hardwareconfig.SetCommandExecutor(mockCmd)
	defer hardwareconfig.ResetCommandExecutor()

	// Load and parse the hardware config
	hwConfigData, err := os.ReadFile("../hardwareconfig/testdata/wpc-hwconfig.yaml")
	assert.NoError(t, err, "Should be able to read hardware config test data")

	var hwConfig ptpv2alpha1.HardwareConfig
	err = yaml.Unmarshal(hwConfigData, &hwConfig)
	assert.NoError(t, err, "Should be able to parse hardware config YAML")

	// Verify the hardware config has the expected structure for our test
	assert.Equal(t, "01-tbc-tr", hwConfig.Spec.RelatedPtpProfileName, "Expected profile name")
	assert.NotNil(t, hwConfig.Spec.Profile.ClockChain, "Expected clock chain")
	assert.NotNil(t, hwConfig.Spec.Profile.ClockChain.Behavior, "Expected behavior")
	assert.NotEmpty(t, hwConfig.Spec.Profile.ClockChain.Behavior.Sources, "Expected behavior sources")

	// Find the PTP source
	var ptpSource *ptpv2alpha1.SourceConfig
	for i, source := range hwConfig.Spec.Profile.ClockChain.Behavior.Sources {
		if source.SourceType == "ptpTimeReceiver" {
			ptpSource = &hwConfig.Spec.Profile.ClockChain.Behavior.Sources[i]
			break
		}
	}
	assert.NotNil(t, ptpSource, "Should find PTP time receiver source")
	assert.Contains(t, ptpSource.PTPTimeReceivers, "ens4f1", "Expected ens4f1 to be monitored")

	// Create hardware config manager and verify it works with our config
	fakeClient := fake.NewClientset()
	hcm := hardwareconfig.NewHardwareConfigManager(fakeClient, "default", nil)
	err = hcm.UpdateHardwareConfig([]ptpv2alpha1.HardwareConfig{hwConfig})
	assert.NoError(t, err, "Should be able to update hardware config")

	// Verify the profile association
	hasConfig := hcm.HasHardwareConfigForProfile(&ptpv1.PtpProfile{
		Name: stringPointer("t-bc_01-tbc-tr"),
	})
	assert.True(t, hasConfig, "Should have hardware config for profile 01-tbc-tr")

	// Get configs for the profile
	profiles := hcm.GetHardwareConfigsForProfile(&ptpv1.PtpProfile{
		Name: stringPointer("t-bc_01-tbc-tr"),
	})
	assert.Len(t, profiles, 1, "Should get exactly one hardware profile")
	assert.NotNil(t, profiles[0].Name, "Hardware profile should have a name")
	assert.Equal(t, "tbc", *profiles[0].Name, "Should get the tbc hardware profile")

	// Get the detector and verify it's properly initialized
	detector := hcm.GetPTPStateDetector()
	assert.NotNil(t, detector, "Should get a valid PTP state detector")

	// Verify monitored ports
	monitoredPorts := detector.GetMonitoredPorts()
	assert.Contains(t, monitoredPorts, "ens4f1", "ens4f1 should be monitored")

	// Test that the detector is ready for use
	t.Run("detector ready for processing", func(t *testing.T) {
		// The detector should be able to handle log processing
		// We'll test this by ensuring it doesn't crash on basic operations
		behaviorRules := detector.GetBehaviorRules()
		assert.NotEmpty(t, behaviorRules, "Should have behavior rules")

		t.Logf("Hardware config loaded successfully with %d monitored ports and %d behavior rules",
			len(monitoredPorts), len(behaviorRules))
	})
}

func TestTBCBMCAAppliesHardwareConditions(t *testing.T) {
	pinPath := filepath.Join(t.TempDir(), "SDP0")
	require.NoError(t, os.WriteFile(pinPath, []byte("unchanged"), 0o644))
	hardwareconfig.SetupMockPtpDeviceResolverWithDevices(map[string][]string{
		"/sys/class/net/eno5/device/ptp/ptp*/pins/SDP0": {pinPath},
	})
	t.Cleanup(hardwareconfig.TeardownMockPtpDeviceResolver)
	hardwareconfig.SetDpllPinsGetter(hardwareconfig.CreateMockDpllPinsGetter(nil, nil))
	t.Cleanup(hardwareconfig.ResetDpllPinsGetter)

	pinState := func(function ptpv2alpha1.PTPPinFunction, channel int64) []ptpv2alpha1.DesiredState {
		return []ptpv2alpha1.DesiredState{{PTPPin: &ptpv2alpha1.PTPPinDesiredState{
			Name: "SDP0", Func: function, Chan: channel, SourceName: "PTP",
		}}}
	}
	hwConfig := ptpv2alpha1.HardwareConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "bmca-test"},
		Spec: ptpv2alpha1.HardwareConfigSpec{
			RelatedPtpProfileName: "test-profile",
			Profile: ptpv2alpha1.HardwareProfile{ClockChain: &ptpv2alpha1.ClockChain{
				Behavior: &ptpv2alpha1.Behavior{
					Sources: []ptpv2alpha1.SourceConfig{{
						Name: "PTP", SourceType: "ptpTimeReceiver", Subsystem: "leader", PTPTimeReceivers: []string{"eno2", "eno3"},
					}},
					Conditions: []ptpv2alpha1.Condition{
						{Name: "lost", Triggers: []ptpv2alpha1.SourceState{{SourceName: "PTP", ConditionType: hardwareconfig.ConditionTypeLost}}, DesiredStates: pinState(ptpv2alpha1.PTPPinFunctionDisabled, 0)},
						{Name: "locked", Triggers: []ptpv2alpha1.SourceState{{SourceName: "PTP", ConditionType: hardwareconfig.ConditionTypeLocked}}, DesiredStates: pinState(ptpv2alpha1.PTPPinFunctionRX, 1)},
					},
				},
				Structure: []ptpv2alpha1.Subsystem{{Name: "leader", Ethernet: []ptpv2alpha1.Ethernet{{Ports: []string{"eno5", "eno2", "eno3"}}}}},
			}},
		},
	}
	hcm := hardwareconfig.NewHardwareConfigManager(fake.NewClientset(), "default", nil)
	require.NoError(t, hcm.UpdateHardwareConfig([]ptpv2alpha1.HardwareConfig{hwConfig}))
	exerciseTBCBMCA(t, hcm, func(enabled bool) {
		t.Helper()
		want := "0 0"
		if enabled {
			want = "1 1"
		}
		got, err := os.ReadFile(pinPath)
		require.NoError(t, err)
		assert.Equal(t, want, string(got))
	})
}

func TestTBCDualUpstream_ActiveTRPort_Helper(t *testing.T) {
	t.Run("returns active port when set", func(t *testing.T) {
		attrs := &tBCProcessAttributes{
			activePort:   "eno3",
			trIfaceNames: []string{"eno2", "eno3"},
		}
		assert.Equal(t, "eno3", attrs.activeTRPort())
	})

	t.Run("falls back to first port when no active", func(t *testing.T) {
		attrs := &tBCProcessAttributes{
			trIfaceNames: []string{"eno2", "eno3"},
		}
		assert.Equal(t, "eno2", attrs.activeTRPort())
	})

	t.Run("returns empty when no ports", func(t *testing.T) {
		attrs := &tBCProcessAttributes{}
		assert.Equal(t, "", attrs.activeTRPort())
	})
}

// TestPtp4lConf_PopulatePtp4lConf_ClockTypeWithCliArgs tests clock_type detection with cliArgs parameter
func TestPtp4lConf_PopulatePtp4lConf_ClockTypeWithCliArgs(t *testing.T) {
	tests := []struct {
		name              string
		config            string
		cliArgs           *string
		expectedClockType event.ClockType
		description       string
	}{
		{
			name:              "OC with -s flag",
			config:            "[global]\n",
			cliArgs:           stringPointer("-s -f /etc/ptp4l.conf"),
			expectedClockType: event.OC,
			description:       "CLI args contain -s flag, should result in OC",
		},
		{
			name:              "OC with -s and single interface",
			config:            "[global]\n[ens1f0]\n",
			cliArgs:           stringPointer("-s"),
			expectedClockType: event.OC,
			description:       "CLI -s with single interface should result in OC",
		},
		{
			name:              "BC with -s and multiple interfaces",
			config:            "[global]\n[ens1f0]\nmasterOnly 0\n[ens1f1]\nmasterOnly 1",
			cliArgs:           stringPointer("-s"),
			expectedClockType: event.BC,
			description:       "CLI -s with multiple interfaces (one slave) should result in BC",
		},
		{
			name:              "GM with masterOnly interfaces",
			config:            "[global]\n[ens1f0]\nmasterOnly 1\n[ens1f1]\nmasterOnly 1",
			cliArgs:           stringPointer("-f /etc/ptp4l.conf"),
			expectedClockType: event.GM,
			description:       "masterOnly interfaces should result in GM",
		},
		{
			name:              "OC with slaveOnly config",
			config:            "[global]\nslaveOnly 1\n",
			cliArgs:           nil,
			expectedClockType: event.OC,
			description:       "slaveOnly in config should result in OC",
		},
		{
			name:              "GM with masterOnly config",
			config:            "[global]\n[ens1f0]\nmasterOnly 1\n",
			cliArgs:           nil,
			expectedClockType: event.GM,
			description:       "masterOnly config should result in GM",
		},
		{
			name:              "OC with -s in middle of args",
			config:            "[global]\n",
			cliArgs:           stringPointer("-f /etc/ptp4l.conf -s -m"),
			expectedClockType: event.OC,
			description:       "CLI -s flag in middle of args should be detected for OC",
		},
		{
			name:              "OC with both -s and slaveOnly",
			config:            "[global]\nslaveOnly 1\n",
			cliArgs:           stringPointer("-s"),
			expectedClockType: event.OC,
			description:       "Both CLI -s and slaveOnly in config should result in OC",
		},
		{
			name:              "GM with empty cliArgs",
			config:            "[global]\n[ens1f0]\nmasterOnly 1\n",
			cliArgs:           stringPointer(""),
			expectedClockType: event.GM,
			description:       "Empty CLI args string with masterOnly should result in GM",
		},
		{
			name:              "BC with serverOnly 0",
			config:            "[global]\n[ens1f0]\nserverOnly 0\n[ens1f1]\nmasterOnly 1\n",
			cliArgs:           nil,
			expectedClockType: event.BC,
			description:       "serverOnly 0 with multiple interfaces should result in BC",
		},
		{
			name:              "OC with clientOnly 1",
			config:            "[global]\n[ens1f0]\nclientOnly 1\n",
			cliArgs:           nil,
			expectedClockType: event.OC,
			description:       "clientOnly 1 should be detected as OC",
		},
		{
			name:              "BC with mixed masterOnly",
			config:            "[global]\n[ens1f0]\nmasterOnly 0\n[ens1f1]\nmasterOnly 1\n",
			cliArgs:           nil,
			expectedClockType: event.BC,
			description:       "Multiple interfaces with mixed masterOnly should result in BC",
		},
		{
			name:              "OC with -s and serverOnly 0",
			config:            "[global]\n[ens1f0]\nserverOnly 0\n",
			cliArgs:           stringPointer("-s"),
			expectedClockType: event.OC,
			description:       "CLI -s with serverOnly 0 and single interface should result in OC",
		},
		{
			name:              "OC with -s and clientOnly 1",
			config:            "[global]\n[ens1f0]\nclientOnly 1\n",
			cliArgs:           stringPointer("-s"),
			expectedClockType: event.OC,
			description:       "CLI -s with clientOnly 1 should result in OC",
		},
		{
			name:              "GM with no slave configuration",
			config:            "[global]\n[ens1f0]\n[ens1f1]\n",
			cliArgs:           stringPointer("-f /etc/ptp4l.conf -m"),
			expectedClockType: event.GM,
			description:       "Multiple interfaces with no explicit master/slave settings should result in GM",
		},
		{
			name:              "OC with --slaveOnly 1",
			config:            "[global]\n",
			cliArgs:           stringPointer("--slaveOnly 1 -f /etc/ptp4l.conf"),
			expectedClockType: event.OC,
			description:       "CLI --slaveOnly 1 should result in OC",
		},
		{
			name:              "OC with --slaveOnly=1",
			config:            "[global]\n",
			cliArgs:           stringPointer("--slaveOnly=1 -f /etc/ptp4l.conf"),
			expectedClockType: event.OC,
			description:       "CLI --slaveOnly=1 should result in OC",
		},
		{
			name:              "GM with --slaveOnly 0",
			config:            "[global]\n[ens1f0]\n",
			cliArgs:           stringPointer("--slaveOnly 0 -f /etc/ptp4l.conf"),
			expectedClockType: event.GM,
			description:       "CLI --slaveOnly 0 should result in GM",
		},
		{
			name:              "GM with --slaveOnly=0",
			config:            "[global]\n[ens1f0]\n",
			cliArgs:           stringPointer("--slaveOnly=0 -f /etc/ptp4l.conf"),
			expectedClockType: event.GM,
			description:       "CLI --slaveOnly=0 should result in GM",
		},
		{
			name:              "OC with --clientOnly 1",
			config:            "[global]\n",
			cliArgs:           stringPointer("--clientOnly 1 -f /etc/ptp4l.conf"),
			expectedClockType: event.OC,
			description:       "CLI --clientOnly 1 should result in OC",
		},
		{
			name:              "OC with --clientOnly=1",
			config:            "[global]\n",
			cliArgs:           stringPointer("--clientOnly=1 -f /etc/ptp4l.conf"),
			expectedClockType: event.OC,
			description:       "CLI --clientOnly=1 should result in OC",
		},
		{
			name:              "GM with --clientOnly 0",
			config:            "[global]\n[ens1f0]\n",
			cliArgs:           stringPointer("--clientOnly 0 -f /etc/ptp4l.conf"),
			expectedClockType: event.GM,
			description:       "CLI --clientOnly 0 should result in GM",
		},
		{
			name:              "GM with --clientOnly=0",
			config:            "[global]\n[ens1f0]\n",
			cliArgs:           stringPointer("--clientOnly=0 -f /etc/ptp4l.conf"),
			expectedClockType: event.GM,
			description:       "CLI --clientOnly=0 should result in GM",
		},
		{
			name:              "OC with --slaveOnly and multiple spaces",
			config:            "[global]\n",
			cliArgs:           stringPointer("--slaveOnly  1 -f /etc/ptp4l.conf"),
			expectedClockType: event.OC,
			description:       "CLI --slaveOnly with multiple spaces should result in OC",
		},
		{
			name:              "OC with --clientOnly and multiple spaces",
			config:            "[global]\n",
			cliArgs:           stringPointer("--clientOnly   1 -f /etc/ptp4l.conf"),
			expectedClockType: event.OC,
			description:       "CLI --clientOnly with multiple spaces should result in OC",
		},
		{
			name:              "OC with -s at end of args",
			config:            "[global]\n",
			cliArgs:           stringPointer("-f /etc/ptp4l.conf -s"),
			expectedClockType: event.OC,
			description:       "CLI -s at end of args should result in OC",
		},
		{
			name:              "OC with -s at start of args",
			config:            "[global]\n",
			cliArgs:           stringPointer("-s -f /etc/ptp4l.conf"),
			expectedClockType: event.OC,
			description:       "CLI -s at start of args should result in OC",
		},
		{
			name:              "OC with only -s flag",
			config:            "[global]\n",
			cliArgs:           stringPointer("-s"),
			expectedClockType: event.OC,
			description:       "CLI with only -s flag should result in OC",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conf := &Ptp4lConf{}
			err := conf.PopulatePtp4lConf(&tt.config, tt.cliArgs)

			assert.NoError(t, err, "PopulatePtp4lConf should not return error")
			assert.Equal(t, tt.expectedClockType, conf.clock_type,
				"Clock type mismatch: expected %v, got %v - %s",
				tt.expectedClockType, conf.clock_type, tt.description)
		})
	}
}

// --- ReadyTracker.Ready() unit tests ---

func makeReadyTracker(processes []process.Process) *ReadyTracker {
	return &ReadyTracker{
		config: true,
		processManager: &ProcessManager{
			process: processes,
		},
	}
}

func readyProc(name string, stopped, hasMetrics bool, delayedStart bool) *ptpProcess {
	p := &ptpProcess{
		ExecProcess:         ExecProcess{name: name},
		hasCollectedMetrics: hasMetrics,
	}
	if stopped {
		p.state = process.Stopped
	}

	if delayedStart {
		p.conditions = map[process.Action]process.Condition{
			process.ActionStart: process.OnStateAndOffsetForCount{
				ConfigName: "ts2phc.0.config",
				Source:     event.TS2PHC,
				State:      event.PTP_LOCKED,
				MaxOffset:  1e9,
				Count:      3,
			},
		}
	}
	return p
}

func asProcesses(procs ...*ptpProcess) []process.Process {
	out := make([]process.Process, len(procs))
	for i, p := range procs {
		out[i] = p
	}
	return out
}

func TestReady_NoProcesses(t *testing.T) {
	rt := makeReadyTracker(nil)
	ok, msg := rt.Ready()
	assert.False(t, ok)
	assert.Contains(t, msg, "No processes")
}

func TestReady_AllRunningWithMetrics(t *testing.T) {
	rt := makeReadyTracker(asProcesses(
		readyProc(event.Ptp4lProcessName, false, true, false),
		readyProc(event.Phc2SysProcessName, false, true, false),
	))
	ok, msg := rt.Ready()
	assert.True(t, ok, msg)
}

func TestReady_StoppedProcessReportsNotReady(t *testing.T) {
	rt := makeReadyTracker(asProcesses(
		readyProc(event.Ptp4lProcessName, false, true, false),
		readyProc(event.Phc2SysProcessName, true, false, false),
	))
	ok, msg := rt.Ready()
	assert.False(t, ok)
	assert.Contains(t, msg, "Stopped")
	assert.Contains(t, msg, event.Phc2SysProcessName)
}

func TestReady_DelayedPhc2sysNotReportedAsStopped(t *testing.T) {
	// phc2sys is intentionally delayed (start condition set): the pod
	// should be considered ready without it.
	rt := makeReadyTracker(asProcesses(
		readyProc(event.Ptp4lProcessName, false, true, false),
		readyProc(event.Phc2SysProcessName, true, false, true),
	))
	ok, msg := rt.Ready()
	assert.True(t, ok, msg)
}

func TestReady_DelayedTs2phcNotReportedAsStopped(t *testing.T) {
	rt := makeReadyTracker(asProcesses(
		readyProc(event.Ptp4lProcessName, false, true, false),
		readyProc(event.Ts2PhcProcessName, true, false, true),
		readyProc(event.Phc2SysProcessName, true, false, true),
	))
	ok, msg := rt.Ready()
	assert.True(t, ok, msg)
}

func TestReady_NilProcessEntrySkipped(t *testing.T) {
	// nil slots in the process slice must not panic.
	rt := makeReadyTracker([]process.Process{
		readyProc(event.Ptp4lProcessName, false, true, false),
		nil,
	})
	ok, msg := rt.Ready()
	assert.True(t, ok, msg)
}

func TestReady_DelayedHaPhc2sysNotReportedAsStopped(t *testing.T) {
	haPhc2sys := readyProc(event.Phc2SysProcessName, true, false, false)
	haPhc2sys.conditions = map[process.Action]process.Condition{
		process.ActionStart: process.Any{Conditions: []process.Condition{
			process.OnStateAndOffsetForCount{ClockID: "ptp4l.0.config", ConfigName: "ptp4l.0.config", Source: event.PTP4l},
			process.OnStateAndOffsetForCount{ClockID: "ptp4l.1.config", ConfigName: "ptp4l.1.config", Source: event.PTP4l},
		}},
	}
	rt := makeReadyTracker(asProcesses(
		readyProc(event.Ptp4lProcessName, false, true, false),
		haPhc2sys,
	))
	ok, msg := rt.Ready()
	assert.True(t, ok, msg)
}

func TestReady_AllProcessesDelayed(t *testing.T) {
	// If every process has a delayed start condition (e.g. a phc2sys-only HA profile
	// where ptp4l lives in separate profiles), the pod must not report ready.
	rt := makeReadyTracker(asProcesses(
		readyProc(event.Phc2SysProcessName, true, false, true),
	))
	ok, msg := rt.Ready()
	assert.False(t, ok)
	assert.Contains(t, msg, "No processes")
}

func TestProcessStatusHelperDoesNotSetMetric(t *testing.T) {
	beforeStatus := testutil.CollectAndCount(ProcessStatus)
	beforeRestart := testutil.CollectAndCount(ProcessRestartCount)

	processStatus("ptp4l", "[ptp4l.0.config]", PtpProcessUp)

	assert.Equal(t, beforeStatus, testutil.CollectAndCount(ProcessStatus),
		"processStatus helper must not write openshift_ptp_process_status; ClockManager owns the gauge")
	assert.Equal(t, beforeRestart, testutil.CollectAndCount(ProcessRestartCount),
		"processStatus helper must not increment process_restart_count")
}

func TestFindProcessesByName(t *testing.T) {
	pm := &ProcessManager{
		process: asProcesses(
			&ptpProcess{ExecProcess: ExecProcess{name: event.Ptp4lProcessName}},
			&ptpProcess{ExecProcess: ExecProcess{name: event.Phc2SysProcessName}},
			&ptpProcess{ExecProcess: ExecProcess{name: event.Ptp4lProcessName}},
		),
	}

	procs := pm.findProcessesByName(event.Ptp4lProcessName)
	assert.Equal(t, 2, len(procs))
	assert.Equal(t, event.Ptp4lProcessName, procs[0].Name())
	assert.Equal(t, event.Ptp4lProcessName, procs[1].Name())

	procs = pm.findProcessesByName(event.Phc2SysProcessName)
	assert.Equal(t, 1, len(procs))

	procs = pm.findProcessesByName("nonexistent")
	assert.Equal(t, 0, len(procs))
}

func TestPtp4lConf_ResolveInterfaceNames(t *testing.T) {
	resolver := network.NewInterfaceResolverWithInterfaces([]string{"ens5f0np0", "ens5f1np1"})

	t.Run("resolves interface sections", func(t *testing.T) {
		conf := &Ptp4lConf{}
		config := "[global]\ndomainNumber 24\n[ens5f0]\nmasterOnly 1\n[ens5f1]\nmasterOnly 0"
		err := conf.PopulatePtp4lConf(&config, nil)
		assert.NoError(t, err)

		conf.ResolveInterfaceNames(resolver)

		for _, section := range conf.sections {
			if section.sectionName == "[ens5f0]" || section.sectionName == "[ens5f1]" {
				t.Errorf("section %s should have been resolved", section.sectionName)
			}
		}
		// Verify resolved names exist
		found := map[string]bool{}
		for _, section := range conf.sections {
			found[section.sectionName] = true
		}
		assert.True(t, found["[ens5f0np0]"], "should have [ens5f0np0] section")
		assert.True(t, found["[ens5f1np1]"], "should have [ens5f1np1] section")
		assert.True(t, found[GlobalSectionName], "should keep [global] section")
	})

	t.Run("skips known sections", func(t *testing.T) {
		conf := &Ptp4lConf{}
		config := "[global]\ndomainNumber 24\n[nmea]\nts2phc.master 1"
		err := conf.PopulatePtp4lConf(&config, nil)
		assert.NoError(t, err)

		conf.ResolveInterfaceNames(resolver)

		found := map[string]bool{}
		for _, section := range conf.sections {
			found[section.sectionName] = true
		}
		assert.True(t, found[GlobalSectionName], "should keep [global]")
		assert.True(t, found[NmeaSectionName], "should keep [nmea]")
	})

	t.Run("no-op when names exist", func(t *testing.T) {
		noChangeResolver := network.NewInterfaceResolverWithInterfaces([]string{"ens5f0", "ens5f1"})
		conf := &Ptp4lConf{}
		config := "[global]\n[ens5f0]\nmasterOnly 1"
		err := conf.PopulatePtp4lConf(&config, nil)
		assert.NoError(t, err)

		conf.ResolveInterfaceNames(noChangeResolver)

		found := map[string]bool{}
		for _, section := range conf.sections {
			found[section.sectionName] = true
		}
		assert.True(t, found["[ens5f0]"], "should keep [ens5f0] unchanged")
	})
}

func TestChronydConf_ResolveInterfaceNames(t *testing.T) {
	conf := "binddevice ens5f0\nbindacqdevice ens5f1\nhwtimestamp ens5f0 minpoll 0\nhwtimestamp *\nallow 10.0.0.0/8"
	profile := &ptpv1.PtpProfile{ChronydConf: &conf}
	resolver := network.NewInterfaceResolverWithInterfaces([]string{"ens5f0np0", "ens5f1np1", "*np0"})
	p := &ptpProcess{ExecProcess: ExecProcess{name: event.ChronydProcessName}}
	output, _, err := p.loadProcessConf(ptpProcessEnv{
		nodeProfile: profile,
		dn:          &Daemon{interfaceResolver: resolver},
	})
	require.NoError(t, err)
	got, _ := output.RenderPtp4lConf()
	lines := strings.Split(got, "\n")
	for _, line := range []string{
		"binddevice ens5f0np0",
		"bindacqdevice ens5f1np1",
		"hwtimestamp ens5f0np0 minpoll 0",
		"hwtimestamp *",
		"allow 10.0.0.0/8",
	} {
		assert.Contains(t, lines, line)
	}
}

func TestTs2PhcConf_ResolveTodSource(t *testing.T) {
	for _, tt := range []struct{ source, want string }{
		{"ens5f0", "ens5f0np0"},
		{"generic", "generic"},
		{"nmea", "nmea"},
		{"/dev/ptp0", "/dev/ptp0"},
	} {
		t.Run(tt.source, func(t *testing.T) {
			conf := "[global]\nts2phc.tod_source " + tt.source + "\n[nmea]\nts2phc.master 1"
			profile := &ptpv1.PtpProfile{Ts2PhcConf: &conf}
			resolver := network.NewInterfaceResolverWithInterfaces([]string{"ens5f0np0", "genericnp0", "nmeanp0"})
			p := &ptpProcess{ExecProcess: ExecProcess{name: event.Ts2PhcProcessName}}
			output, _, err := p.loadProcessConf(ptpProcessEnv{
				nodeProfile: profile,
				dn:          &Daemon{interfaceResolver: resolver},
			})
			require.NoError(t, err)
			got, _ := output.RenderPtp4lConf()
			assert.Contains(t, strings.Split(got, "\n"), "ts2phc.tod_source "+tt.want)
			assert.Contains(t, got, "[nmea]")
		})
	}
}

func TestUpdateClockStateMetrics(t *testing.T) {
	origNodeName := NodeName
	defer func() { NodeName = origNodeName }()

	NodeName = "test-node"
	RegisterMetrics(NodeName)
	ClockState.Reset()

	tests := []struct {
		state    string
		expected float64
	}{
		{FREERUN, event.ClockStateFreerun},
		{LOCKED, event.ClockStateLocked},
		{HOLDOVER, event.ClockStateHoldover},
	}

	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			updateClockStateMetrics(event.Ptp4lProcessName, testDUTLeadingIface, tt.state, "")
			gauge, err := ClockState.GetMetricWith(prometheus.Labels{
				labelProcess: event.Ptp4lProcessName, labelNode: NodeName, labelIface: testDUTLeadingIface,
			})
			assert.NoError(t, err)
			actual := testutil.ToFloat64(gauge)
			assert.Equal(t, tt.expected, actual,
				"clock_state for %s should be %v", tt.state, tt.expected)
		})
	}
}

func TestDeleteMetrics_CleansPhc2sysProcessSeries(t *testing.T) {
	origNodeName := NodeName
	defer func() { NodeName = origNodeName }()

	NodeName = "test-node"
	RegisterMetrics(NodeName)

	tests := []struct {
		name          string
		metricCfgName string
	}{
		{
			name:          "non-HA label from ptp4l message tag",
			metricCfgName: "ptp4l.1.config",
		},
		{
			name:          "legacy label from phc2sys config name",
			metricCfgName: "phc2sys.1.config",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ProcessStatus.Reset()
			ProcessRestartCount.Reset()

			UpdateProcessStatusMetrics(event.Phc2SysProcessName, tt.metricCfgName, PtpProcessUp)
			assert.Equal(t, 1, testutil.CollectAndCount(ProcessStatus))
			assert.Equal(t, 1, testutil.CollectAndCount(ProcessRestartCount))

			deleteMetrics(nil, nil, event.Phc2SysProcessName, "phc2sys.1.config", "[ptp4l.1.config:{level}]")

			assert.Equal(t, 0, testutil.CollectAndCount(ProcessStatus))
			assert.Equal(t, 0, testutil.CollectAndCount(ProcessRestartCount))
		})
	}
}

func TestGetPTPThreshold(t *testing.T) {
	profileName := "test-profile"

	tests := []struct {
		name             string
		profile          ptpv1.PtpProfile
		expectedMax      int64
		expectedMin      int64
		expectedHoldover int64
	}{
		{
			// MinOffsetThreshold is deprecated: the default (no explicit
			// PtpClockThreshold on the profile) now resolves to 0, not a
			// synthetic -100.
			name: "default threshold without ntpfailover",
			profile: ptpv1.PtpProfile{
				Name: &profileName,
			},
			expectedMax:      100,
			expectedMin:      0,
			expectedHoldover: 5,
		},
		{
			name: "ntpfailover with gnssFailover enabled uses looser threshold",
			profile: ptpv1.PtpProfile{
				Name: &profileName,
				Plugins: map[string]*apiextensions.JSON{
					"ntpfailover": {Raw: []byte(`{"gnssFailover": true}`)},
				},
			},
			expectedMax:      1000,
			expectedMin:      0,
			expectedHoldover: 5,
		},
		{
			name: "ntpfailover with gnssFailover disabled uses standard threshold",
			profile: ptpv1.PtpProfile{
				Name: &profileName,
				Plugins: map[string]*apiextensions.JSON{
					"ntpfailover": {Raw: []byte(`{"gnssFailover": false}`)},
				},
			},
			expectedMax:      100,
			expectedMin:      0,
			expectedHoldover: 5,
		},
		{
			// MinOffsetThreshold is deprecated and is never populated on the
			// returned threshold, even when the profile explicitly sets it.
			name: "explicit PtpClockThreshold takes precedence over ntpfailover",
			profile: ptpv1.PtpProfile{
				Name: &profileName,
				PtpClockThreshold: &ptpv1.PtpClockThreshold{
					HoldOverTimeout:    10,
					MaxOffsetThreshold: 500,
					MinOffsetThreshold: -500,
				},
				Plugins: map[string]*apiextensions.JSON{
					"ntpfailover": {Raw: []byte(`{"gnssFailover": true}`)},
				},
			},
			expectedMax:      500,
			expectedMin:      0,
			expectedHoldover: 10,
		},
		{
			name: "explicit PtpClockThreshold with omitted or zero MinOffsetThreshold",
			profile: ptpv1.PtpProfile{
				Name: &profileName,
				PtpClockThreshold: &ptpv1.PtpClockThreshold{
					HoldOverTimeout:    10,
					MaxOffsetThreshold: 500,
				},
			},
			expectedMax:      500,
			expectedMin:      0,
			expectedHoldover: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getPTPThreshold(&tt.profile)
			assert.Equal(t, tt.expectedMax, result.MaxOffsetThreshold)
			assert.Equal(t, tt.expectedMin, result.MinOffsetThreshold)
			assert.Equal(t, tt.expectedHoldover, result.HoldOverTimeout)
		})
	}
}

func Test_shouldFreeRun(t *testing.T) {
	threshold100 := &ptpv1.PtpClockThreshold{
		MaxOffsetThreshold: 100,
		MinOffsetThreshold: -100,
	}
	threshold100OmittedMin := &ptpv1.PtpClockThreshold{
		MaxOffsetThreshold: 100,
		MinOffsetThreshold: 0,
	}
	threshold100AsymmetricMin := &ptpv1.PtpClockThreshold{
		MaxOffsetThreshold: 100,
		MinOffsetThreshold: -50,
	}

	tests := []struct {
		name         string
		currentState event.PTPState
		offset       float64
		threshold    *ptpv1.PtpClockThreshold
		expected     bool
	}{
		{
			name:         "already in HOLDOVER -> false",
			currentState: event.PTP_HOLDOVER,
			offset:       500,
			threshold:    threshold100,
			expected:     false,
		},
		{
			name:         "already in FREERUN -> false",
			currentState: event.PTP_FREERUN,
			offset:       500,
			threshold:    threshold100,
			expected:     false,
		},
		{
			name:         "in-range positive offset -> false",
			currentState: event.PTP_LOCKED,
			offset:       50,
			threshold:    threshold100,
			expected:     false,
		},
		{
			name:         "in-range negative offset -> false",
			currentState: event.PTP_LOCKED,
			offset:       -50,
			threshold:    threshold100,
			expected:     false,
		},
		{
			name:         "out-of-range positive offset -> true",
			currentState: event.PTP_LOCKED,
			offset:       150,
			threshold:    threshold100,
			expected:     true,
		},
		{
			name:         "out-of-range negative offset -> true",
			currentState: event.PTP_LOCKED,
			offset:       -150,
			threshold:    threshold100,
			expected:     true,
		},
		{
			name:         "exact positive boundary offset (non-inclusive) -> true",
			currentState: event.PTP_LOCKED,
			offset:       100,
			threshold:    threshold100,
			expected:     true,
		},
		{
			name:         "exact negative boundary offset (non-inclusive) -> true",
			currentState: event.PTP_LOCKED,
			offset:       -100,
			threshold:    threshold100,
			expected:     true,
		},
		{
			name:         "backward-compat: omitted MinOffsetThreshold in-range -> false",
			currentState: event.PTP_LOCKED,
			offset:       50,
			threshold:    threshold100OmittedMin,
			expected:     false,
		},
		{
			name:         "backward-compat: omitted MinOffsetThreshold out-of-range negative -> true",
			currentState: event.PTP_LOCKED,
			offset:       -150,
			threshold:    threshold100OmittedMin,
			expected:     true,
		},
		{
			name:         "backward-compat: asymmetric MinOffsetThreshold behaves identically using abs(offset) < Max -> false",
			currentState: event.PTP_LOCKED,
			offset:       -70,
			threshold:    threshold100AsymmetricMin,
			expected:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := shouldFreeRun(tt.currentState, tt.offset, tt.threshold)
			assert.Equal(t, tt.expected, actual, "shouldFreeRun result mismatch")
		})
	}
}

// TestDaemon_PopulateHAInterfaces tests that populateHAInterfaces correctly populates
// the haProfile map in phc2sys process with interface names from referenced ptp4l profiles.
func TestDaemon_PopulateHAInterfaces(t *testing.T) {
	skip, teardownTest := testhelpers.SetupForTestBCPTPHA()
	defer teardownTest()
	if skip {
		t.Skip("BC PTP-HA is not supported")
	}

	// Create two regular ptp4l profiles with interfaces
	p1 := ptpv1.PtpProfile{
		Name: stringPointer("profile1"),
	}
	p2 := ptpv1.PtpProfile{
		Name: stringPointer("profile2"),
	}
	// Create a phc2sys HA profile that references the two ptp4l profiles
	p3 := ptpv1.PtpProfile{
		Name:        stringPointer("ha_profile1"),
		PtpSettings: map[string]string{PTP_HA_IDENTIFIER: "profile1,profile2"},
	}

	processManager := NewProcessManager()

	// Set up interfaces for profile1
	ifaces1 := config.IFaces{
		{
			Name:     "ens2f2",
			IsMaster: false,
			Source:   "",
			PhcId:    "phcid-2",
		},
	}

	// Set up interfaces for profile2
	ifaces2 := config.IFaces{
		{
			Name:     "ens3f2",
			IsMaster: false,
			Source:   "",
			PhcId:    "phcid-2",
		},
	}

	// Create test processes in the process manager
	// Use "ptp4l" as the process name for ptp4l processes
	processManager.SetTestProfileProcess("ptp4l", ifaces1, "socket1", "ptp4l.0.config", p1)
	processManager.SetTestProfileProcess("ptp4l", ifaces2, "socket2", "ptp4l.1.config", p2)
	processManager.SetTestProfileProcess("phc2sys", config.IFaces{}, "", "phc2sys.0.config", p3)

	// Get the phc2sys process to verify haProfile population
	var phc2sysProc *ptpProcess
	for _, proc := range processManager.process {
		if ptpProc, ok := proc.(*ptpProcess); ok && ptpProc.name == "phc2sys" {
			phc2sysProc = ptpProc
			break
		}
	}
	require.NotNil(t, phc2sysProc, "phc2sys process should exist")

	// Initialize haProfile map (normally done in NewPhc2sysProcess)
	phc2sysProc.haProfile = make(map[string][]string)

	// Create OSClockConfigs to simulate the HA configuration
	topology := ProfileTopology{
		haProfile:    "ha_profile1",
		haMembers:    []string{"profile1", "profile2"},
		profileRunID: map[string]int{"profile1": 0, "profile2": 1},
	}

	// Create daemon and call populateHAInterfaces
	dd := NewDaemonForTests(&ReadyTracker{}, processManager)
	dd.populateHAInterfaces(&topology)

	// Verify that haProfile was populated correctly
	require.NotNil(t, phc2sysProc.haProfile, "haProfile should not be nil")
	require.Equal(t, 2, len(phc2sysProc.haProfile), "haProfile should have 2 entries")
	assert.Equal(t, []string{"ens2f2"}, phc2sysProc.haProfile["profile1"], "profile1 should map to ens2f2")
	assert.Equal(t, []string{"ens3f2"}, phc2sysProc.haProfile["profile2"], "profile2 should map to ens3f2")
}

// TestPhc2sysProcess_HASocketOptions tests that haSocketOpts generates correct
// socket options when HA is enabled, properly referencing all profiles.
func TestPhc2sysProcess_HASocketOptions(t *testing.T) {
	skip, teardownTest := testhelpers.SetupForTestBCPTPHA()
	defer teardownTest()
	if skip {
		t.Skip("BC PTP-HA is not supported")
	}

	// Create ProfileTopology to simulate HA configuration with two ptp4l profiles
	topology := ProfileTopology{
		haProfile:    "ha_profile1",
		haMembers:    []string{"profile1", "profile2"},
		profileRunID: map[string]int{"profile1": 0, "profile2": 1},
	}

	// Call haSocketOpts which should generate socket options for both referenced profiles
	socketOpts := haSocketOpts(&topology)

	// Should contain socket options for both referenced profiles
	assert.NotEmpty(t, socketOpts, "socketOpts should not be empty")
	assert.Contains(t, socketOpts, "-z", "socketOpts should contain socket flag")
	assert.Contains(t, socketOpts, "ptp4l.0.socket", "socketOpts should reference ptp4l.0.socket")
	assert.Contains(t, socketOpts, "ptp4l.1.socket", "socketOpts should reference ptp4l.1.socket")
	// Verify format - should have two -z options separated by space
	parts := strings.Fields(socketOpts)
	assert.Equal(t, 4, len(parts), "socketOpts should have 4 parts: -z socket1 -z socket2")
	assert.Equal(t, "-z", parts[0], "First part should be -z")
	assert.Equal(t, "-z", parts[2], "Third part should be -z")
}
