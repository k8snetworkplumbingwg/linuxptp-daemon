package daemon

// This tests daemon private functions

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bigkevmcd/go-configparser"
	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/alias"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	dpll "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/hardwareconfig"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/synce"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
	"github.com/stretchr/testify/assert"
	"k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/yaml"
)

const (
	testDUTLeadingIface = "ens2f0"
	testDUTUpstream1    = "ens2f1"
	testDUTUpstream2    = "ens2f3"
	testDUTClockIDKey   = "clockId[ens2f0]"
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
		close(leap.LeapMgr.Close)
		// Sleep to allow context to switch
		time.Sleep(100 * time.Millisecond)
		assert.Nil(t, leap.LeapMgr)
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
	osClockConfigs := NewOSClockConfigs([]ptpv1.PtpProfile{*profile})
	err := dn.applyNodePtpProfile(0, profile, &osClockConfigs)
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
			expectedProcesses: []string{ptp4lProcessName},
		},
		{
			dataFile:          "testdata/profile-tbc-tr.yaml",
			expectedProcesses: []string{ptp4lProcessName, ptp4lProcessName, ts2phcProcessName, phc2sysProcessName},
		},
	}
	stopCh := make(<-chan struct{})
	assert.NoError(t, leap.MockLeapFile())
	defer func() {
		close(leap.LeapMgr.Close)
		// Sleep to allow context to switch
		time.Sleep(100 * time.Millisecond)
		assert.Nil(t, leap.LeapMgr)
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

	for _, test := range tests {
		mkPath(t)
		profile, err := loadProfile(test.dataFile)
		assert.NoError(t, err)
		// Will assert inside in case of error:
		osClockConfigs := NewOSClockConfigs([]ptpv1.PtpProfile{*profile})
		err = dn.applyNodePtpProfile(0, profile, &osClockConfigs)
		assert.NoError(t, err)

		// Ensure for T-BC that phc2sys has a non-Immediate start condition (delayed start)
		actualProcesses := []string{}
		for _, p := range dn.processManager.process {
			actualProcesses = append(actualProcesses, p.Name())
			if p.Name() == phc2sysProcessName {
				cond := process.GetCondition(p, process.ActionStart, process.Immediate{})
				offsetCond, ok := cond.(process.OnStateAndOffsetForCount)
				assert.True(t, ok, "T-BC phc2sys must wait on ptp4l lock/offset, got %s", cond)
				assert.Equal(t, event.PTP4l, offsetCond.Source)
				assert.True(t, strings.HasPrefix(offsetCond.ConfigName, "ptp4l."), offsetCond.ConfigName)
			}
			if p.Name() == ts2phcProcessName {
				cond := process.GetCondition(p, process.ActionStart, process.Immediate{})
				assert.NotEqual(t, process.Immediate{}, cond, "Ensure ts2phc is startup-delayed for T-BC")
			}
		}
		assert.ElementsMatch(t, test.expectedProcesses, actualProcesses, "Ensure T-BC has the required processes prepared (%s)", test.dataFile)
		clean(t)
	}
}

func Test_applyProfile_TGM(t *testing.T) {
	defer clean(t)
	mkPath(t)

	stopCh := make(<-chan struct{})
	assert.NoError(t, leap.MockLeapFile())
	defer func() {
		close(leap.LeapMgr.Close)
		time.Sleep(100 * time.Millisecond)
		assert.Nil(t, leap.LeapMgr)
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
	osClockConfigs := NewOSClockConfigs([]ptpv1.PtpProfile{*profile})
	err = dn.applyNodePtpProfile(0, profile, &osClockConfigs)
	assert.NoError(t, err)

	var ts2phcProc *ptpProcess
	var ptp4lProc *ptpProcess
	for _, p := range dn.processManager.process {
		ptp, ok := p.(*ptpProcess)
		if !ok {
			continue
		}
		switch ptp.Name() {
		case ts2phcProcessName:
			ts2phcProc = ptp
			cond := process.GetCondition(ptp, process.ActionStart, process.Immediate{})
			_, isTBCDelay := cond.(process.OnPluginEvent)
			assert.False(t, isTBCDelay, "T-GM ts2phc must not have a delayed start condition")
		case ptp4lProcessName:
			ptp4lProc = ptp
		case phc2sysProcessName:
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
		assert.Contains(t, depNames, GPSD_PROCESSNAME, "gpsd must be a dependent process of ts2phc for T-GM")
		assert.Contains(t, depNames, GPSPIPE_PROCESSNAME, "gpspipe must be a dependent process of ts2phc for T-GM")

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

// Note: TestGetPTPClockId_MissingLeadingInterface has been moved to ptp_process_test.go
// See ptp_process_test.go for the complete set of TestGetPTPClockId_* tests

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

// Note: TestGetPTPThreshold has been moved to ptp_process_internal_test.go
// See ptp_process_internal_test.go for the test

// Note: Test_shouldFreeRun has been moved to log_parsing_test.go
// See log_parsing_test.go for the test
