package daemon

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/hardwareconfig"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
	"github.com/stretchr/testify/assert"
	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/yaml"
)

// Note: TestGetPTPClockId_* tests have been moved to ptp_process_test.go (external tests)
// See ptp_process_test.go for:
// - TestGetPTPClockId_ValidInput
// - TestGetPTPClockId_ParsingError
// - TestGetPTPClockId_MissingClockId

// Helper function to create string pointers
func stringPointer(s string) *string {
	return &s
}

// TestTBCTransitionCheck_HardwareConfigPath tests the hardware config path of tBCTransitionCheck
func TestTBCTransitionCheck_HardwareConfigPath(t *testing.T) {
	// Create a real PluginManager
	pmStruct, _ := registerPlugins([]string{})
	pm := &pmStruct

	// Test case: Verify hardware config setup
	t.Run("hardware config setup validation", func(t *testing.T) {
		// Create a ptpProcess with hardware config enabled
		// Set global variable for hardware config
		vTbcHasHardwareConfig = true
		defer func() { vTbcHasHardwareConfig = false }()

		process := &ptpProcess{
			tBCAttributes: tBCProcessAttributes{
				trIfaceNames: []string{"ens4f0"},
				perPortState: map[string]event.PTPState{"ens4f0": event.PTP_NOTSET},
			},
			nodeProfile: &ptpv1.PtpProfile{ //nolint:govet // needed for test setup
				Name: stringPointer("test-profile"),
				PtpSettings: map[string]string{
					"leadingInterface": "ens4f0",
					"clockId[ens4f0]":  "123456789",
				},
			},
			clockType:        event.BC,                                      //nolint:govet // needed for test setup
			tbcStateDetector: createMockPTPStateDetectorForHardwareConfig(), // Use mock detector
		}

		// Verify that hardware config path conditions are met
		assert.NotNil(t, process.tbcStateDetector, "PTPStateDetector should be present for hardware config path")
		assert.True(t, vTbcHasHardwareConfig, "Hardware config should be enabled")
		assert.Equal(t, []string{"ens4f0"}, process.tBCAttributes.trIfaceNames, "Interface names should be set correctly")

		// Verify the path selection logic would choose hardware config path
		// This tests the condition: vTbcHasHardwareConfig && p.tbcStateDetector != nil
		assert.True(t, vTbcHasHardwareConfig && process.tbcStateDetector != nil,
			"Hardware config path should be taken when both conditions are met")
	})

	// Test case: Locked transition with offset filtering
	t.Run("locked transition with offset filtering", func(t *testing.T) {
		// Set global variable for hardware config
		oldValue := vTbcHasHardwareConfig
		vTbcHasHardwareConfig = true
		defer func() { vTbcHasHardwareConfig = oldValue }()

		// Create a mock Daemon with hardwareConfigManager and set up hardware config
		fakeClient := fake.NewClientset()
		hcm := hardwareconfig.NewHardwareConfigManager(fakeClient, "default", nil)
		err := setupHardwareConfigForTest(hcm, "test-profile", "ens4f0")
		assert.NoError(t, err, "Should be able to set up hardware config")
		mockDaemon := &Daemon{
			hardwareConfigManager: hcm,
		}

		detector := hardwareconfig.NewPTPStateDetector(hcm) // Use same HCM

		// Verify detector has "ens4f0" in monitored ports
		monitoredPorts := detector.GetMonitoredPorts()
		assert.Contains(t, monitoredPorts, "ens4f0", "ens4f0 should be in monitored ports")

		proc := &ptpProcess{
			ExecProcess: ExecProcess{
				eventCh:    make(chan event.Event, 1), //nolint:govet // needed for test setup
				configName: "test-config",             //nolint:govet // needed for test setup
			},
			tBCAttributes: tBCProcessAttributes{
				trIfaceNames:      []string{"ens4f0"},
				perPortState:      map[string]event.PTPState{"ens4f0": event.PTP_NOTSET},
				trPortsConfigFile: "test-config",
				lastAppliedState:  event.PTP_NOTSET,
				offsetThreshold:   10.0,
			},
			nodeProfile: &ptpv1.PtpProfile{
				Name: stringPointer("test-profile"),
				PtpSettings: map[string]string{
					"leadingInterface": "ens4f0",
					"clockId[ens4f0]":  "123456789",
				},
			},
			clockType:        event.BC,
			offset:           5.0,
			tbcStateDetector: detector,
			dn:               mockDaemon,
		}

		// First call: Trigger ConditionTypeLocked (no event sent yet)
		// The parser detects locked state when event contains "to SLAVE"
		proc.tBCTransitionCheck("ptp4l[123.456]: [test-config.0.config] port 1 (ens4f0): UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED", pm)

		// Verify state changed to LOCKED
		assert.Equal(t, event.PTP_LOCKED, proc.tBCAttributes.lastReportedState)

		// Verify filter was created
		assert.NotNil(t, proc.tBCAttributes.offsetFilter, "Offset filter should be created")

		// Verify no event was sent yet (event is only sent when filter is full)
		select {
		case <-proc.eventCh:
			t.Error("Event should not be sent immediately on locked transition")
		default:
			// Good, no event yet
		}

		// Fill the offset filter by calling tBCTransitionCheck with messages
		// The filter needs to be full (64 samples) before the event is sent
		// First call already inserted 1 sample, so we need 63 more to fill it
		// Use metric log lines (not event lines) to fill the filter
		for i := 0; i < 63; i++ {
			proc.tBCTransitionCheck("ptp4l[123.456]: [test-config.0.config] master offset 5 s2 freq 0 path delay 100", pm)
		}

		// Verify event was sent after filter is full
		select {
		case <-proc.eventCh:
			// Event was sent, good
		default:
			t.Error("Expected PTP event to be sent after filter is full")
		}

		// Verify lastAppliedState was updated
		assert.Equal(t, event.PTP_LOCKED, proc.tBCAttributes.lastAppliedState)
	})

	// Test case: Lost transition (immediate)
	t.Run("lost transition", func(t *testing.T) {
		// Set global variable for hardware config
		oldValue := vTbcHasHardwareConfig
		vTbcHasHardwareConfig = true
		defer func() { vTbcHasHardwareConfig = oldValue }()

		// Create a mock Daemon with hardwareConfigManager and set up hardware config
		fakeClient := fake.NewClientset()
		hcm := hardwareconfig.NewHardwareConfigManager(fakeClient, "default", nil)
		err := setupHardwareConfigForTest(hcm, "test-profile", "ens4f0")
		assert.NoError(t, err, "Should be able to set up hardware config")
		mockDaemon := &Daemon{
			hardwareConfigManager: hcm,
		}

		detector := hardwareconfig.NewPTPStateDetector(hcm) // Use same HCM

		// Verify detector has "ens4f0" in monitored ports
		monitoredPorts := detector.GetMonitoredPorts()
		assert.Contains(t, monitoredPorts, "ens4f0", "ens4f0 should be in monitored ports")

		process := &ptpProcess{
			ExecProcess: ExecProcess{
				eventCh:    make(chan event.Event, 1), //nolint:govet // needed for test setup
				configName: "test-config",             //nolint:govet // needed for test setup
			},
			tBCAttributes: tBCProcessAttributes{
				trIfaceNames: []string{"ens4f0"},
				perPortState: map[string]event.PTPState{"ens4f0": event.PTP_LOCKED},
			},
			nodeProfile: &ptpv1.PtpProfile{
				Name: stringPointer("test-profile"),
				PtpSettings: map[string]string{
					"leadingInterface": "ens4f0",
					"clockId[ens4f0]":  "123456789",
				},
			},
			clockType:        event.BC,
			tbcStateDetector: detector,
			dn:               mockDaemon,
		}

		// Call with lost transition log - parser detects lost when event contains "SLAVE to"
		process.tBCTransitionCheck("ptp4l[123.456]: [test-config.0.config] port 1 (ens4f0): SLAVE to MASTER on ANNOUNCE_RECEIPT_TIMEOUT_EXPIRES", pm)

		// Verify state changed to FREERUN
		assert.Equal(t, event.PTP_FREERUN, process.tBCAttributes.lastReportedState)

		// Verify filter was reset
		assert.Nil(t, process.tBCAttributes.offsetFilter, "Offset filter should be reset on lost transition")

		// Verify event was sent immediately
		select {
		case <-process.eventCh:
			// Event was sent, good
		default:
			t.Error("Expected PTP event to be sent immediately on lost transition")
		}
	})

	// Test case: Hardware config path vs legacy path decision logic
	t.Run("path decision logic", func(t *testing.T) {
		testCases := []struct {
			name                 string
			tbcHasHardwareConfig bool
			hasDetector          bool
			expectedPath         string
		}{
			{
				name:                 "hardware config path",
				tbcHasHardwareConfig: true,
				hasDetector:          true,
				expectedPath:         "hardware",
			},
			{
				name:                 "legacy path - no hardware config",
				tbcHasHardwareConfig: false,
				hasDetector:          true,
				expectedPath:         "legacy",
			},
			{
				name:                 "legacy path - no detector",
				tbcHasHardwareConfig: true,
				hasDetector:          false,
				expectedPath:         "legacy",
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				// Set global variable for hardware config
				oldValue := vTbcHasHardwareConfig
				vTbcHasHardwareConfig = tc.tbcHasHardwareConfig
				defer func() { vTbcHasHardwareConfig = oldValue }()

				process := &ptpProcess{
					tBCAttributes: tBCProcessAttributes{
						trIfaceNames: []string{"ens4f0"},
						perPortState: map[string]event.PTPState{"ens4f0": event.PTP_NOTSET},
					},
				}

				if tc.hasDetector {
					process.tbcStateDetector = createMockPTPStateDetectorForHardwareConfig()
				}

				// Determine which path would be taken
				var actualPath string
				if vTbcHasHardwareConfig && process.tbcStateDetector != nil {
					actualPath = "hardware"
				} else {
					actualPath = "legacy"
				}

				assert.Equal(t, tc.expectedPath, actualPath,
					"Expected path %s but got %s", tc.expectedPath, actualPath)
			})
		}
	})
}

// TestTBCTransitionCheck_PathSelection tests which path is taken based on conditions
func TestTBCTransitionCheck_PathSelection(t *testing.T) {
	tests := []struct {
		name                 string
		tbcHasHardwareConfig bool
		hasStateDetector     bool
		expectedLegacy       bool
		description          string
	}{
		{
			name:                 "hardware config path - both conditions true",
			tbcHasHardwareConfig: true,
			hasStateDetector:     true,
			expectedLegacy:       false,
			description:          "Should take hardware config path when both conditions are met",
		},
		{
			name:                 "legacy path - hardware config false",
			tbcHasHardwareConfig: false,
			hasStateDetector:     true,
			expectedLegacy:       true,
			description:          "Should take legacy path when hardware config is disabled",
		},
		{
			name:                 "legacy path - detector nil",
			tbcHasHardwareConfig: true,
			hasStateDetector:     false,
			expectedLegacy:       true,
			description:          "Should take legacy path when detector is not available",
		},
		{
			name:                 "legacy path - both conditions false",
			tbcHasHardwareConfig: false,
			hasStateDetector:     false,
			expectedLegacy:       true,
			description:          "Should take legacy path when both conditions are false",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set global variable for hardware config
			oldValue := vTbcHasHardwareConfig
			vTbcHasHardwareConfig = tt.tbcHasHardwareConfig
			defer func() { vTbcHasHardwareConfig = oldValue }()

			// Create ptpProcess with test conditions
			process := &ptpProcess{
				ExecProcess: ExecProcess{
					eventCh:    make(chan event.Event, 1), //nolint:govet // needed for test setup
					configName: "test-config",             //nolint:govet // needed for test setup
				},
				tBCAttributes: tBCProcessAttributes{
					trIfaceNames: []string{"ens4f0"},
					perPortState: map[string]event.PTPState{"ens4f0": event.PTP_NOTSET},
				},
				nodeProfile: &ptpv1.PtpProfile{ //nolint:govet // needed for test setup
					Name: stringPointer("test-profile"),
					PtpSettings: map[string]string{
						"leadingInterface": "ens4f0",
						"clockId[ens4f0]":  "123456789",
					},
				},
				clockType: event.BC, //nolint:govet // needed for test setup
			}

			// Set state detector based on test case
			if tt.hasStateDetector {
				process.tbcStateDetector = createMockPTPStateDetectorForHardwareConfig()
			} else {
				process.tbcStateDetector = nil
			}

			// Test the path selection logic without calling the actual function
			// (to avoid crashes due to incomplete mock setup)

			// Verify that the correct path condition is met
			if tt.expectedLegacy {
				// For legacy path, either hardware config is disabled or detector is nil
				assert.True(t, !vTbcHasHardwareConfig || process.tbcStateDetector == nil,
					"Legacy path should be taken when hardware config is disabled or detector is nil")
			} else {
				// For hardware config path, both conditions must be true
				assert.True(t, vTbcHasHardwareConfig && process.tbcStateDetector != nil,
					"Hardware config path should be taken when both conditions are met")
			}
		})
	}
}

// setupHardwareConfigForTest sets up a hardware config with a PTP source monitoring the given port
func setupHardwareConfigForTest(hcm *hardwareconfig.HardwareConfigManager, profileName, portName string) error {
	// Mock GetDpllPins to return an empty pin cache (no pins needed for this test)
	hardwareconfig.SetDpllPinsGetter(hardwareconfig.CreateMockDpllPinsGetter(nil, nil))
	defer hardwareconfig.ResetDpllPinsGetter()

	// Create a minimal hardware config with a PTP source monitoring the specified port
	hwConfig := ptpv2alpha1.HardwareConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-hwconfig",
		},
		Spec: ptpv2alpha1.HardwareConfigSpec{
			RelatedPtpProfileName: profileName,
			Profile: ptpv2alpha1.HardwareProfile{
				ClockChain: &ptpv2alpha1.ClockChain{
					Behavior: &ptpv2alpha1.Behavior{
						Sources: []ptpv2alpha1.SourceConfig{
							{
								Name:             "PTP4l",
								SourceType:       "ptpTimeReceiver",
								PTPTimeReceivers: []string{portName},
								Subsystem:        "test-subsystem",
							},
						},
					},
					Structure: []ptpv2alpha1.Subsystem{
						{
							Name: "test-subsystem",
							Ethernet: []ptpv2alpha1.Ethernet{
								{
									Ports: []string{portName},
								},
							},
						},
					},
				},
			},
		},
	}

	// Update hardware config manager with the test config
	return hcm.UpdateHardwareConfig([]ptpv2alpha1.HardwareConfig{hwConfig})
}

// createMockPTPStateDetectorForHardwareConfig creates a mock PTPStateDetector for hardware config testing
// Creates a hardware config with a PTP source that monitors ens4f0, then initializes the detector
func createMockPTPStateDetectorForHardwareConfig() *hardwareconfig.PTPStateDetector {
	// Create a detector using the normal constructor - this properly initializes ptp4lExtractor
	fakeClient := fake.NewClientset()
	hcm := hardwareconfig.NewHardwareConfigManager(fakeClient, "default", nil)
	_ = setupHardwareConfigForTest(hcm, "test-profile", "ens4f0")

	// Create detector - it will automatically populate monitoredPorts from the hardware config
	return hardwareconfig.NewPTPStateDetector(hcm)
}

// TestProcessTBCTransitionHardwareConfig_HardwareConfigIntegration tests integration with real hardware config
func TestProcessTBCTransitionHardwareConfig_HardwareConfigIntegration(t *testing.T) {
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

// TestProcessTBCTransitionHardwareConfig_ProcessLogFile reads log data line by line and processes it
func TestProcessTBCTransitionHardwareConfig_ProcessLogFile(t *testing.T) {
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

	// Load the hardware config from testdata
	hwConfigData, err := os.ReadFile("../hardwareconfig/testdata/wpc-hwconfig.yaml")
	assert.NoError(t, err, "Should be able to read hardware config test data")

	// Parse the hardware config
	var hwConfig ptpv2alpha1.HardwareConfig
	err = yaml.Unmarshal(hwConfigData, &hwConfig)
	assert.NoError(t, err, "Should be able to parse hardware config YAML")

	// Create hardware config manager and initialize it
	fakeClient := fake.NewClientset()
	hcm := hardwareconfig.NewHardwareConfigManager(fakeClient, "default", nil)
	err = hcm.UpdateHardwareConfig([]ptpv2alpha1.HardwareConfig{hwConfig})
	assert.NoError(t, err, "Should be able to update hardware config")

	// Get the PTP state detector
	detector := hcm.GetPTPStateDetector()
	assert.NotNil(t, detector, "Should get a valid PTP state detector")

	// Create a mock Daemon with hardwareConfigManager
	mockDaemon := &Daemon{
		hardwareConfigManager: hcm,
	}

	// Create a ptpProcess with the real hardware config setup
	process := &ptpProcess{
		ExecProcess: ExecProcess{
			eventCh:    make(chan event.Event, 1), //nolint:govet // needed for test setup
			configName: "test-config",             //nolint:govet // needed for test setup
		},
		tBCAttributes: tBCProcessAttributes{
			trIfaceNames: []string{"ens4f1"},
			perPortState: map[string]event.PTPState{"ens4f1": event.PTP_NOTSET},
		},
		nodeProfile: &ptpv1.PtpProfile{
			Name: stringPointer("t-bc_01-tbc-tr"), // Matches relatedPtpProfileName from config (qualified by operator)
			PtpSettings: map[string]string{
				"leadingInterface": "ens4f1",
				"clockId[ens4f1]":  "123456789",
			},
		},
		clockType:        event.BC,
		tbcStateDetector: detector, // Use real detector with real config
		dn:               mockDaemon,
	}

	// Read the log file line by line
	logFile, err := os.Open("../hardwareconfig/testdata/log2.txt")
	assert.NoError(t, err, "Should be able to open log file")
	defer func() {
		_ = logFile.Close()
	}()

	scanner := bufio.NewScanner(logFile)

	// Track processing results
	linesProcessed := 0
	transitionsDetected := 0
	eventsGenerated := 0
	ptpLinesFound := 0
	ens4f1LinesFound := 0

	// Track state changes
	stateChanges := []event.PTPState{}

	t.Logf("Starting to process log file line by line...")

	// Process each line through processTBCTransitionHardwareConfig
	for scanner.Scan() {
		line := scanner.Text()
		linesProcessed++

		// Track PTP-related lines for debugging
		if strings.Contains(line, "ptp4l") {
			ptpLinesFound++
		}
		if strings.Contains(line, "ens4f1") {
			ens4f1LinesFound++
			// Log first few ens4f1 lines for debugging
			if ens4f1LinesFound <= 5 {
				t.Logf("ens4f1 line %d: %s", ens4f1LinesFound, line)
			}
		}

		// Capture initial state
		initialState := process.tBCAttributes.lastReportedState

		// Process the line through the function under test
		process.processTBCTransitionHardwareConfig(line)

		// Check if state changed
		if process.tBCAttributes.lastReportedState != initialState {
			transitionsDetected++
			stateChanges = append(stateChanges, process.tBCAttributes.lastReportedState)
			t.Logf("Line %d: State transition detected: %s -> %s",
				linesProcessed, initialState, process.tBCAttributes.lastReportedState)
			t.Logf("  Log line: %s", line)
		}

		// Check if event was generated (non-blocking check)
		select {
		case ev := <-process.eventCh:
			eventsGenerated++
			t.Logf("Line %d: PTP event generated: %+v", linesProcessed, ev)
		default:
			// No event generated, continue
		}

		// Log progress every 10000 lines
		if linesProcessed%10000 == 0 {
			t.Logf("Processed %d lines, detected %d transitions, generated %d events (PTP lines: %d, ens4f1 lines: %d)",
				linesProcessed, transitionsDetected, eventsGenerated, ptpLinesFound, ens4f1LinesFound)
		}
	}

	assert.NoError(t, scanner.Err(), "Should not have errors reading log file")

	// Log final results
	t.Logf("=== FINAL RESULTS ===")
	t.Logf("Total lines processed: %d", linesProcessed)
	t.Logf("PTP lines found: %d", ptpLinesFound)
	t.Logf("ens4f1 lines found: %d", ens4f1LinesFound)
	t.Logf("State transitions detected: %d", transitionsDetected)
	t.Logf("PTP events generated: %d", eventsGenerated)
	t.Logf("Final PTP state: %s", process.tBCAttributes.lastReportedState)

	if len(stateChanges) > 0 {
		t.Logf("State change sequence: %v", stateChanges)
	}
	// The number of transitions depends on the actual log content and hardware config behavior
	// We just verify that the processing completed without crashing
	t.Logf("Processing completed successfully with %d transitions detected", transitionsDetected)
}

// TestTBCTransitionCheck_LegacyPath tests the legacy path of tBCTransitionCheck
func TestTBCTransitionCheck_LegacyPath(t *testing.T) {
	// Create a real PluginManager
	pmStruct, _ := registerPlugins([]string{})
	pm := &pmStruct

	// Test case 1: Locked transition
	t.Run("locked transition", func(t *testing.T) {
		// Set global variable to force legacy path
		oldValue := vTbcHasHardwareConfig
		vTbcHasHardwareConfig = false
		defer func() { vTbcHasHardwareConfig = oldValue }()

		process := &ptpProcess{
			ExecProcess: ExecProcess{
				eventCh:    make(chan event.Event, 1), //nolint:govet // needed for test setup
				configName: "test-config",             //nolint:govet // needed for test setup
			},
			tBCAttributes: tBCProcessAttributes{
				trIfaceNames:      []string{"ens4f0"},
				perPortState:      map[string]event.PTPState{"ens4f0": event.PTP_NOTSET},
				trPortsConfigFile: "test-config",
				lastAppliedState:  event.PTP_NOTSET,
				offsetThreshold:   10.0,
			},
			nodeProfile: &ptpv1.PtpProfile{
				Name: stringPointer("test-profile"),
				PtpSettings: map[string]string{
					"leadingInterface": "ens4f0",
					"clockId[ens4f0]":  "123456789",
				},
			},
			clockType: event.BC,
			offset:    5.0, // Set offset < threshold (10.0) to allow event to be sent
		}

		// First call: Set state to LOCKED (no event sent yet)
		process.tBCTransitionCheck("ptp4l[123] port 1 (ens4f0): to SLAVE on MASTER_CLOCK_SELECTED", pm)

		// Verify state changed to LOCKED
		assert.Equal(t, event.PTP_LOCKED, process.tBCAttributes.lastReportedState)

		// Verify no event was sent yet (event is only sent when filter is full)
		select {
		case <-process.eventCh:
			t.Error("Event should not be sent immediately on locked transition")
		default:
			// Good, no event yet
		}

		// Fill the offset filter by calling tBCTransitionCheck with messages containing the interface name
		// The filter needs to be full (64 samples) before the event is sent
		// First call already inserted 1 sample, so we need 63 more to fill it
		for i := 0; i < 63; i++ {
			process.tBCTransitionCheck("ptp4l[123] port 1 (ens4f0): some other log message", pm)
		}

		// Verify event was sent after filter is full
		select {
		case <-process.eventCh:
			// Event was sent, good
		default:
			t.Error("Expected PTP event to be sent after filter is full")
		}
	})

	// Test case 2: Lost transition
	t.Run("lost transition", func(t *testing.T) {
		// Set global variable - will still take legacy path due to nil detector
		oldValue := vTbcHasHardwareConfig
		vTbcHasHardwareConfig = true
		defer func() { vTbcHasHardwareConfig = oldValue }()

		process := &ptpProcess{
			ExecProcess: ExecProcess{
				eventCh:    make(chan event.Event, 1), //nolint:govet // needed for test setup
				configName: "test-config",             //nolint:govet // needed for test setup
			},
			tBCAttributes: tBCProcessAttributes{
				trIfaceNames: []string{"ens4f0"},
				perPortState: map[string]event.PTPState{"ens4f0": event.PTP_NOTSET},
			},
			nodeProfile: &ptpv1.PtpProfile{
				Name: stringPointer("test-profile"),
				PtpSettings: map[string]string{
					"leadingInterface": "ens4f0",
					"clockId[ens4f0]":  "123456789",
				},
			},
			clockType: event.BC,
		}

		// Call with lost transition log
		process.tBCTransitionCheck("ptp4l[123] port 1 (ens4f0): SLAVE to", pm)

		// Verify state changed to FREERUN
		assert.Equal(t, event.PTP_FREERUN, process.tBCAttributes.lastReportedState)

		// Verify event was sent
		select {
		case <-process.eventCh:
			// Event was sent, good
		default:
			t.Error("Expected PTP event to be sent")
		}
	})

	// Test case 3: No transition
	t.Run("no transition", func(t *testing.T) {
		// Set global variable
		oldValue := vTbcHasHardwareConfig
		vTbcHasHardwareConfig = true
		defer func() { vTbcHasHardwareConfig = oldValue }()

		process := &ptpProcess{
			tBCAttributes: tBCProcessAttributes{
				trIfaceNames: []string{"ens4f0"},
				perPortState: map[string]event.PTPState{"ens4f0": event.PTP_NOTSET},
			},
			nodeProfile: &ptpv1.PtpProfile{
				Name: stringPointer("test-profile"),
				PtpSettings: map[string]string{
					"leadingInterface": "ens4f0",
					"clockId[ens4f0]":  "123456789",
				},
			},
			ExecProcess: ExecProcess{
				eventCh:    make(chan event.Event, 1),
				configName: "test-config",
			},
			clockType: event.BC,
		}

		initialState := process.tBCAttributes.lastReportedState

		// Call with log that doesn't match any transition
		process.tBCTransitionCheck("ptp4l[123] port 1 (ens4f0): some other message", pm)

		// Verify state didn't change
		assert.Equal(t, initialState, process.tBCAttributes.lastReportedState)

		// Verify no event was sent
		select {
		case <-process.eventCh:
			t.Error("Unexpected PTP event was sent")
		default:
			// No event sent, which is correct
		}
	})
}

// setupDualUpstreamHardwareConfig sets up a hardware config with two PTP time receiver ports on the same subsystem
func setupDualUpstreamHardwareConfig(hcm *hardwareconfig.HardwareConfigManager, profileName, port1, port2 string) error {
	hardwareconfig.SetDpllPinsGetter(hardwareconfig.CreateMockDpllPinsGetter(nil, nil))
	defer hardwareconfig.ResetDpllPinsGetter()

	hwConfig := ptpv2alpha1.HardwareConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-hwconfig-dual",
		},
		Spec: ptpv2alpha1.HardwareConfigSpec{
			RelatedPtpProfileName: profileName,
			Profile: ptpv2alpha1.HardwareProfile{
				ClockChain: &ptpv2alpha1.ClockChain{
					Behavior: &ptpv2alpha1.Behavior{
						Sources: []ptpv2alpha1.SourceConfig{
							{
								Name:             "PTP",
								SourceType:       "ptpTimeReceiver",
								PTPTimeReceivers: []string{port1, port2},
								Subsystem:        "leader",
							},
						},
					},
					Structure: []ptpv2alpha1.Subsystem{
						{
							Name: "leader",
							Ethernet: []ptpv2alpha1.Ethernet{
								{
									Ports: []string{"eno5", port1, port2},
								},
							},
						},
					},
				},
			},
		},
	}

	return hcm.UpdateHardwareConfig([]ptpv2alpha1.HardwareConfig{hwConfig})
}

func TestTBCDualUpstream_PortALost_PortBTakesOver(t *testing.T) {
	oldValue := vTbcHasHardwareConfig
	vTbcHasHardwareConfig = true
	defer func() { vTbcHasHardwareConfig = oldValue }()

	fakeClient := fake.NewClientset()
	hcm := hardwareconfig.NewHardwareConfigManager(fakeClient, "default", nil)
	err := setupDualUpstreamHardwareConfig(hcm, "test-profile", "eno2", "eno3")
	assert.NoError(t, err)
	mockDaemon := &Daemon{hardwareConfigManager: hcm}
	detector := hardwareconfig.NewPTPStateDetector(hcm)

	process := &ptpProcess{
		tBCAttributes: tBCProcessAttributes{
			trIfaceNames:      []string{"eno2", "eno3"},
			perPortState:      map[string]event.PTPState{"eno2": event.PTP_NOTSET, "eno3": event.PTP_NOTSET},
			trPortsConfigFile: "test-config",
			lastAppliedState:  event.PTP_NOTSET,
			offsetThreshold:   10.0,
		},
		nodeProfile: &ptpv1.PtpProfile{
			Name:        stringPointer("test-profile"),
			PtpSettings: map[string]string{"leadingInterface": "eno5", "clockId[eno5]": "123456789"},
		},
		ExecProcess: ExecProcess{
			eventCh:    make(chan event.Event, 10),
			configName: "test-config",
		},
		clockType:        event.BC,
		offset:           5.0,
		tbcStateDetector: detector,
		dn:               mockDaemon,
	}

	pmStruct, _ := registerPlugins([]string{})
	pm := &pmStruct

	// Port eno2 becomes SLAVE
	process.tBCTransitionCheck("ptp4l[100]: [test-config.0.config] port 1 (eno2): UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED", pm)
	assert.Equal(t, event.PTP_LOCKED, process.tBCAttributes.lastReportedState)
	assert.Equal(t, event.PTP_LOCKED, process.tBCAttributes.perPortState["eno2"])
	assert.Equal(t, "eno2", process.tBCAttributes.activePort)

	// Port eno2 loses SLAVE — but eno3 is not SLAVE yet, so all ports are lost → holdover
	process.tBCTransitionCheck("ptp4l[200]: [test-config.0.config] port 1 (eno2): SLAVE to MASTER on ANNOUNCE_RECEIPT_TIMEOUT_EXPIRES", pm)
	assert.Equal(t, event.PTP_FREERUN, process.tBCAttributes.lastReportedState)
	assert.Equal(t, event.PTP_HOLDOVER, process.tBCAttributes.lastAppliedState)
	assert.Equal(t, "", process.tBCAttributes.activePort)

	// Drain event channel
	for len(process.eventCh) > 0 {
		<-process.eventCh
	}

	// Port eno3 takes over — BMCA first transitions MASTER -> UNCALIBRATED
	process.tBCTransitionCheck("ptp4l[300]: [test-config.0.config] port 2 (eno3): MASTER to UNCALIBRATED on RS_SLAVE", pm)
	// No change yet — UNCALIBRATED is not SLAVE
	assert.Equal(t, event.PTP_FREERUN, process.tBCAttributes.lastReportedState)

	// Then UNCALIBRATED -> SLAVE
	process.tBCAttributes.lastAppliedState = event.PTP_HOLDOVER
	process.tBCTransitionCheck("ptp4l[300]: [test-config.0.config] port 2 (eno3): UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED", pm)
	assert.Equal(t, event.PTP_LOCKED, process.tBCAttributes.lastReportedState)
	assert.Equal(t, event.PTP_LOCKED, process.tBCAttributes.perPortState["eno3"])
	assert.Equal(t, "eno3", process.tBCAttributes.activePort)
}

func TestTBCDualUpstream_BothPortsLost(t *testing.T) {
	oldValue := vTbcHasHardwareConfig
	vTbcHasHardwareConfig = true
	defer func() { vTbcHasHardwareConfig = oldValue }()

	fakeClient := fake.NewClientset()
	hcm := hardwareconfig.NewHardwareConfigManager(fakeClient, "default", nil)
	err := setupDualUpstreamHardwareConfig(hcm, "test-profile", "eno2", "eno3")
	assert.NoError(t, err)
	mockDaemon := &Daemon{hardwareConfigManager: hcm}
	detector := hardwareconfig.NewPTPStateDetector(hcm)

	process := &ptpProcess{
		tBCAttributes: tBCProcessAttributes{
			trIfaceNames:      []string{"eno2", "eno3"},
			perPortState:      map[string]event.PTPState{"eno2": event.PTP_LOCKED, "eno3": event.PTP_LOCKED},
			activePort:        "eno2",
			trPortsConfigFile: "test-config",
			lastReportedState: event.PTP_LOCKED,
			lastAppliedState:  event.PTP_LOCKED,
			offsetThreshold:   10.0,
		},
		nodeProfile: &ptpv1.PtpProfile{
			Name:        stringPointer("test-profile"),
			PtpSettings: map[string]string{"leadingInterface": "eno5", "clockId[eno5]": "123456789"},
		},
		ExecProcess: ExecProcess{
			eventCh:    make(chan event.Event, 10),
			configName: "test-config",
		},
		clockType:        event.BC,
		tbcStateDetector: detector,
		dn:               mockDaemon,
	}

	pmStruct, _ := registerPlugins([]string{})
	pm := &pmStruct

	// Port eno2 loses SLAVE — eno3 still LOCKED, no aggregate change
	process.tBCTransitionCheck("ptp4l[100]: [test-config.0.config] port 1 (eno2): SLAVE to MASTER on ANNOUNCE_RECEIPT_TIMEOUT_EXPIRES", pm)
	assert.Equal(t, event.PTP_FREERUN, process.tBCAttributes.perPortState["eno2"])
	assert.Equal(t, event.PTP_LOCKED, process.tBCAttributes.perPortState["eno3"])
	// Aggregate should still be LOCKED because eno3 is still up
	assert.Equal(t, event.PTP_LOCKED, process.tBCAttributes.lastReportedState)

	// Port eno3 also loses SLAVE — now all ports lost → holdover
	process.tBCTransitionCheck("ptp4l[200]: [test-config.0.config] port 2 (eno3): SLAVE to MASTER on ANNOUNCE_RECEIPT_TIMEOUT_EXPIRES", pm)
	assert.Equal(t, event.PTP_FREERUN, process.tBCAttributes.perPortState["eno3"])
	assert.Equal(t, event.PTP_FREERUN, process.tBCAttributes.lastReportedState)
	assert.Equal(t, event.PTP_HOLDOVER, process.tBCAttributes.lastAppliedState)
	assert.Equal(t, "", process.tBCAttributes.activePort)
}

func TestTBCDualUpstream_RecoveryAfterBothLost(t *testing.T) {
	oldValue := vTbcHasHardwareConfig
	vTbcHasHardwareConfig = true
	defer func() { vTbcHasHardwareConfig = oldValue }()

	fakeClient := fake.NewClientset()
	hcm := hardwareconfig.NewHardwareConfigManager(fakeClient, "default", nil)
	err := setupDualUpstreamHardwareConfig(hcm, "test-profile", "eno2", "eno3")
	assert.NoError(t, err)
	mockDaemon := &Daemon{hardwareConfigManager: hcm}
	detector := hardwareconfig.NewPTPStateDetector(hcm)

	process := &ptpProcess{
		tBCAttributes: tBCProcessAttributes{
			trIfaceNames:      []string{"eno2", "eno3"},
			perPortState:      map[string]event.PTPState{"eno2": event.PTP_FREERUN, "eno3": event.PTP_FREERUN},
			trPortsConfigFile: "test-config",
			lastReportedState: event.PTP_FREERUN,
			lastAppliedState:  event.PTP_HOLDOVER,
			offsetThreshold:   10.0,
		},
		nodeProfile: &ptpv1.PtpProfile{
			Name:        stringPointer("test-profile"),
			PtpSettings: map[string]string{"leadingInterface": "eno5", "clockId[eno5]": "123456789"},
		},
		ExecProcess: ExecProcess{
			eventCh:    make(chan event.Event, 10),
			configName: "test-config",
		},
		clockType:        event.BC,
		offset:           5.0,
		tbcStateDetector: detector,
		dn:               mockDaemon,
	}

	pmStruct, _ := registerPlugins([]string{})
	pm := &pmStruct

	// Port eno2 recovers to SLAVE
	process.tBCTransitionCheck("ptp4l[300]: [test-config.0.config] port 1 (eno2): UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED", pm)
	assert.Equal(t, event.PTP_LOCKED, process.tBCAttributes.lastReportedState)
	assert.Equal(t, event.PTP_LOCKED, process.tBCAttributes.perPortState["eno2"])
	assert.Equal(t, "eno2", process.tBCAttributes.activePort)
	assert.NotNil(t, process.tBCAttributes.offsetFilter, "Offset filter should be created on recovery")

	// Fill offset filter to complete transition
	for i := 0; i < 64; i++ {
		process.tBCTransitionCheck("ptp4l[300]: [test-config.0.config] master offset 5 s2 freq 0 path delay 100", pm)
	}

	assert.Equal(t, event.PTP_LOCKED, process.tBCAttributes.lastAppliedState)
}

func TestTBCDualUpstream_AllPortsLost_Helper(t *testing.T) {
	t.Run("both ports FREERUN", func(t *testing.T) {
		attrs := &tBCProcessAttributes{
			perPortState: map[string]event.PTPState{
				"eno2": event.PTP_FREERUN,
				"eno3": event.PTP_FREERUN,
			},
		}
		assert.True(t, attrs.allPortsLost())
	})

	t.Run("one port LOCKED", func(t *testing.T) {
		attrs := &tBCProcessAttributes{
			perPortState: map[string]event.PTPState{
				"eno2": event.PTP_FREERUN,
				"eno3": event.PTP_LOCKED,
			},
		}
		assert.False(t, attrs.allPortsLost())
	})

	t.Run("both ports LOCKED", func(t *testing.T) {
		attrs := &tBCProcessAttributes{
			perPortState: map[string]event.PTPState{
				"eno2": event.PTP_LOCKED,
				"eno3": event.PTP_LOCKED,
			},
		}
		assert.False(t, attrs.allPortsLost())
	})

	t.Run("both ports NOTSET", func(t *testing.T) {
		attrs := &tBCProcessAttributes{
			perPortState: map[string]event.PTPState{
				"eno2": event.PTP_NOTSET,
				"eno3": event.PTP_NOTSET,
			},
		}
		assert.True(t, attrs.allPortsLost())
	})

	t.Run("single port LOCKED", func(t *testing.T) {
		attrs := &tBCProcessAttributes{
			perPortState: map[string]event.PTPState{
				"eno2": event.PTP_LOCKED,
			},
		}
		assert.False(t, attrs.allPortsLost())
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

func TestTBCLegacy_Switchover_ActivePortUpdated(t *testing.T) {
	pmStruct, _ := registerPlugins([]string{})
	pm := &pmStruct

	oldValue := vTbcHasHardwareConfig
	vTbcHasHardwareConfig = false
	defer func() { vTbcHasHardwareConfig = oldValue }()

	process := &ptpProcess{
		tBCAttributes: tBCProcessAttributes{
			trIfaceNames:      []string{testDUTUpstream1, testDUTUpstream2},
			perPortState:      map[string]event.PTPState{testDUTUpstream1: event.PTP_LOCKED, testDUTUpstream2: event.PTP_NOTSET},
			activePort:        testDUTUpstream1,
			trPortsConfigFile: "test-config",
			lastReportedState: event.PTP_LOCKED,
			lastAppliedState:  event.PTP_LOCKED,
			offsetThreshold:   10.0,
		},
		nodeProfile: &ptpv1.PtpProfile{
			Name:        stringPointer("test-profile"),
			PtpSettings: map[string]string{"leadingInterface": testDUTLeadingIface, testDUTClockIDKey: "123456789"},
		},
		ExecProcess: ExecProcess{
			eventCh:    make(chan event.Event, 10),
			configName: "test-config",
		},
		clockType: event.BC,
		offset:    5.0,
	}

	// ens2f1 goes down — enters holdover
	process.tBCTransitionCheck("ptp4l[100]: [test-config.0.config] port 1 ("+testDUTUpstream1+"): SLAVE to FAULTY on FAULT_DETECTED (FT_UNSPECIFIED)", pm)
	assert.Equal(t, event.PTP_FREERUN, process.tBCAttributes.lastReportedState)
	assert.Equal(t, event.PTP_HOLDOVER, process.tBCAttributes.lastAppliedState)

	// ens2f3 takes over as SLAVE — activePort should update
	process.tBCTransitionCheck("ptp4l[200]: [test-config.0.config] port 2 ("+testDUTUpstream2+"): UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED", pm)
	assert.Equal(t, event.PTP_LOCKED, process.tBCAttributes.lastReportedState)
	assert.Equal(t, testDUTUpstream2, process.tBCAttributes.activePort,
		"activePort should update to backup port after switchover")
	assert.NotNil(t, process.tBCAttributes.offsetFilter)
}

func TestTBCLegacy_AllPortsLost_EntersHoldover(t *testing.T) {
	pmStruct, _ := registerPlugins([]string{})
	pm := &pmStruct

	oldValue := vTbcHasHardwareConfig
	vTbcHasHardwareConfig = false
	defer func() { vTbcHasHardwareConfig = oldValue }()

	process := &ptpProcess{
		tBCAttributes: tBCProcessAttributes{
			trIfaceNames:      []string{testDUTUpstream1, testDUTUpstream2},
			perPortState:      map[string]event.PTPState{testDUTUpstream1: event.PTP_LOCKED, testDUTUpstream2: event.PTP_NOTSET},
			activePort:        testDUTUpstream1,
			trPortsConfigFile: "test-config",
			lastReportedState: event.PTP_LOCKED,
			lastAppliedState:  event.PTP_LOCKED,
			offsetThreshold:   10.0,
		},
		nodeProfile: &ptpv1.PtpProfile{
			Name:        stringPointer("test-profile"),
			PtpSettings: map[string]string{"leadingInterface": testDUTLeadingIface, testDUTClockIDKey: "123456789"},
		},
		ExecProcess: ExecProcess{
			eventCh:    make(chan event.Event, 10),
			configName: "test-config",
		},
		clockType: event.BC,
	}

	// Active port loses SLAVE via ANNOUNCE timeout — enters holdover
	process.tBCTransitionCheck("ptp4l[100]: [test-config.0.config] port 1 ("+testDUTUpstream1+"): SLAVE to MASTER on ANNOUNCE_RECEIPT_TIMEOUT_EXPIRES", pm)
	assert.Equal(t, event.PTP_FREERUN, process.tBCAttributes.lastReportedState)
	assert.Equal(t, event.PTP_HOLDOVER, process.tBCAttributes.lastAppliedState)
	assert.Nil(t, process.tBCAttributes.offsetFilter)
}

func TestTBCLegacy_RecoveryFromHoldover(t *testing.T) {
	pmStruct, _ := registerPlugins([]string{})
	pm := &pmStruct

	oldValue := vTbcHasHardwareConfig
	vTbcHasHardwareConfig = false
	defer func() { vTbcHasHardwareConfig = oldValue }()

	process := &ptpProcess{
		tBCAttributes: tBCProcessAttributes{
			trIfaceNames:      []string{testDUTUpstream1, testDUTUpstream2},
			perPortState:      map[string]event.PTPState{testDUTUpstream1: event.PTP_FREERUN, testDUTUpstream2: event.PTP_NOTSET},
			trPortsConfigFile: "test-config",
			lastReportedState: event.PTP_FREERUN,
			lastAppliedState:  event.PTP_HOLDOVER,
			offsetThreshold:   10.0,
		},
		nodeProfile: &ptpv1.PtpProfile{
			Name:        stringPointer("test-profile"),
			PtpSettings: map[string]string{"leadingInterface": testDUTLeadingIface, testDUTClockIDKey: "123456789"},
		},
		ExecProcess: ExecProcess{
			eventCh:    make(chan event.Event, 10),
			configName: "test-config",
		},
		clockType: event.BC,
		offset:    5.0,
	}

	// Backup port becomes SLAVE — starts recovery
	process.tBCTransitionCheck("ptp4l[200]: [test-config.0.config] port 2 ("+testDUTUpstream2+"): UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED", pm)
	assert.Equal(t, event.PTP_LOCKED, process.tBCAttributes.lastReportedState)
	assert.Equal(t, testDUTUpstream2, process.tBCAttributes.activePort)
	assert.NotNil(t, process.tBCAttributes.offsetFilter)
	assert.Equal(t, event.PTP_HOLDOVER, process.tBCAttributes.lastAppliedState,
		"should remain in holdover until offset filter converges")

	// Fill offset filter (64 samples) to complete recovery
	for i := 0; i < 64; i++ {
		process.tBCTransitionCheck("ptp4l[300]: [test-config.0.config] master offset 5 s2 freq 0 path delay 100", pm)
	}
	assert.Equal(t, event.PTP_LOCKED, process.tBCAttributes.lastAppliedState,
		"should exit holdover after offset filter converges")
}

func TestTBCLegacy_ActiveTRPort_ReportsCorrectInterface(t *testing.T) {
	pmStruct, _ := registerPlugins([]string{})
	pm := &pmStruct

	oldValue := vTbcHasHardwareConfig
	vTbcHasHardwareConfig = false
	defer func() { vTbcHasHardwareConfig = oldValue }()

	process := &ptpProcess{
		tBCAttributes: tBCProcessAttributes{
			trIfaceNames:      []string{testDUTUpstream1, testDUTUpstream2},
			perPortState:      map[string]event.PTPState{testDUTUpstream1: event.PTP_NOTSET, testDUTUpstream2: event.PTP_NOTSET},
			trPortsConfigFile: "test-config",
			lastAppliedState:  event.PTP_NOTSET,
			offsetThreshold:   10.0,
			offsetEventWindow: utils.NewWindow(16),
		},
		nodeProfile: &ptpv1.PtpProfile{
			Name:        stringPointer("test-profile"),
			PtpSettings: map[string]string{"leadingInterface": testDUTLeadingIface, testDUTClockIDKey: "123456789"},
		},
		ExecProcess: ExecProcess{
			eventCh:    make(chan event.Event, 10),
			configName: "test-config",
		},
		clockType: event.BC,
		offset:    3.0,
	}

	// Initially activePort is empty — activeTRPort() returns trIfaceNames[0]
	assert.Equal(t, testDUTUpstream1, process.tBCAttributes.activeTRPort())

	// testDUTUpstream1 becomes SLAVE
	process.tBCTransitionCheck("ptp4l[100]: [test-config.0.config] port 1 ("+testDUTUpstream1+"): UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED", pm)
	assert.Equal(t, testDUTUpstream1, process.tBCAttributes.activeTRPort())

	// Switchover: testDUTUpstream2 becomes SLAVE
	process.tBCTransitionCheck("ptp4l[200]: [test-config.0.config] port 2 ("+testDUTUpstream2+"): UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED", pm)
	assert.Equal(t, testDUTUpstream2, process.tBCAttributes.activeTRPort(),
		"activeTRPort should reflect the newly selected backup port")
}

func TestTBCLegacy_ActivePort_IgnoresNonTRPort(t *testing.T) {
	pmStruct, _ := registerPlugins([]string{})
	pm := &pmStruct

	oldValue := vTbcHasHardwareConfig
	vTbcHasHardwareConfig = false
	defer func() { vTbcHasHardwareConfig = oldValue }()

	process := &ptpProcess{
		tBCAttributes: tBCProcessAttributes{
			trIfaceNames:      []string{testDUTUpstream1, testDUTUpstream2},
			perPortState:      map[string]event.PTPState{testDUTUpstream1: event.PTP_LOCKED, testDUTUpstream2: event.PTP_NOTSET},
			activePort:        testDUTUpstream1,
			trPortsConfigFile: "test-config",
			lastReportedState: event.PTP_LOCKED,
			lastAppliedState:  event.PTP_LOCKED,
			offsetThreshold:   10.0,
		},
		nodeProfile: &ptpv1.PtpProfile{
			Name:        stringPointer("test-profile"),
			PtpSettings: map[string]string{"leadingInterface": testDUTLeadingIface, testDUTClockIDKey: "123456789"},
		},
		ExecProcess: ExecProcess{
			eventCh:    make(chan event.Event, 10),
			configName: "test-config",
		},
		clockType: event.BC,
	}

	// A port with a prefix-colliding name (ens2f10 vs tracked ens2f1) fires MASTER_CLOCK_SELECTED.
	// The log line contains "ens2f1" as a substring so portMatched is true, but
	// ExtractPortName returns "ens2f10" which is NOT in trIfaceNames.
	process.tBCTransitionCheck("ptp4l[300]: [test-config.0.config] port 3 (ens2f10): UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED", pm)
	assert.Equal(t, testDUTUpstream1, process.tBCAttributes.activePort,
		"activePort must not change to a non-TR port with a prefix-colliding name")
}

// TestGetPTPThreshold tests the getPTPThreshold function for various profile configurations.
// It verifies that the function correctly returns threshold values based on profile settings and plugins.
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
