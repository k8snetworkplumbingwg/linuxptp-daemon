package intel

import (
	"errors"
	"fmt"
	"os"
	"testing"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
)

func Test_E810(t *testing.T) {
	p, d := E810("e810")
	assert.NotNil(t, p)
	assert.NotNil(t, d)

	p, d = E810("not_e810")
	assert.Nil(t, p)
	assert.Nil(t, d)
}

func Test_initInternalDelays(t *testing.T) {
	delays, err := InitInternalDelays("E810-XXVDA4T")
	assert.NoError(t, err)
	assert.Equal(t, "E810-XXVDA4T", delays.PartType)
	assert.Len(t, delays.ExternalInputs, 3)
	assert.Len(t, delays.ExternalOutputs, 3)
}

func Test_initInternalDelays_BadPart(t *testing.T) {
	_, err := InitInternalDelays("Dummy")
	assert.Error(t, err)
}

func Test_ProcessProfileTGMNew(t *testing.T) {
	unitTest = true
	profile, err := loadProfile("./testdata/profile-tgm.yaml")
	assert.NoError(t, err)
	p, d := E810("e810")
	err = p.OnPTPConfigChange(d, profile)
	assert.NoError(t, err)
}

// Test that the profile with no phase inputs is processed correctly
func Test_ProcessProfileTBCNoPhaseInputs(t *testing.T) {
	unitTest = true
	profile, err := loadProfile("./testdata/profile-tbc-no-input-delays.yaml")
	assert.NoError(t, err)
	p, d := E810("e810")
	err = p.OnPTPConfigChange(d, profile)
	assert.NoError(t, err)
}

func Test_ProcessProfileTbcE810(t *testing.T) {
	// Setup filesystem mock for TBC profile (3 devices with pins)
	mockFS, restore := setupMockFS()
	defer restore()
	phcEntries := []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}

	// profile-tbc.yaml has pins for ens4f0, ens5f0, ens8f0 (3 devices)
	for i := 0; i < 3; i++ {
		// Each device needs ReadDir + 4 pin writes (SMA1, SMA2, U.FL1, U.FL2)
		mockFS.ExpectReadDir("", phcEntries, nil) // Wildcard path
		for j := 0; j < 4; j++ {
			mockFS.ExpectWriteFile("", []byte(""), os.FileMode(0o666), nil)
		}
	}

	// Add extra operations for EnableE810Outputs and other calls
	for i := 0; i < 10; i++ {
		mockFS.ExpectReadDir("", phcEntries, nil)                       // Extra ReadDir calls
		mockFS.ExpectWriteFile("", []byte(""), os.FileMode(0o666), nil) // Extra WriteFile calls
	}

	// Set unitTest for MockPins() call
	unitTest = true
	defer func() { unitTest = false }()

	// Can read test profile
	profile, err := loadProfile("./testdata/profile-tbc.yaml")
	assert.NoError(t, err)

	// Can run PTP config change handler without errors
	p, d := E810("e810")
	err = p.OnPTPConfigChange(d, profile)
	assert.NoError(t, err)
	ccData := clockChain.(*ClockChain)
	assert.Equal(t, ClockTypeTBC, ccData.Type, "identified a wrong clock type")
	assert.Equal(t, "5799633565432596414", ccData.LeadingNIC.DpllClockID, "identified a wrong clock ID ")
	assert.Equal(t, 9, len(ccData.LeadingNIC.Pins), "wrong number of configurable pins")
	assert.Equal(t, "ens4f1", ccData.LeadingNIC.UpstreamPort, "wrong upstream port")
	// Note: Intentionally NOT calling mockFS.VerifyAllCalls(t) because the mockFS is set up permissibly, not for enforcing
}

func Test_ProcessProfileTtscE810(t *testing.T) {
	// Setup filesystem mock for T-TSC profile (1 device with pins)
	mockFS, restore := setupMockFS()
	defer restore()
	phcEntries := []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}

	// profile-t-tsc.yaml has pins for ens4f0 only
	mockFS.ExpectReadDir("", phcEntries, nil) // One ReadDir
	for i := 0; i < 4; i++ {                  // 4 pin writes
		mockFS.ExpectWriteFile("", []byte(""), os.FileMode(0o666), nil)
	}

	// Add extra operations for EnableE810Outputs and other calls
	for i := 0; i < 10; i++ {
		mockFS.ExpectReadDir("", phcEntries, nil)                       // Extra ReadDir calls
		mockFS.ExpectWriteFile("", []byte(""), os.FileMode(0o666), nil) // Extra WriteFile calls
	}

	// Set unitTest for MockPins() call
	unitTest = true
	defer func() { unitTest = false }()

	// Can read test profile
	profile, err := loadProfile("./testdata/profile-t-tsc.yaml")
	assert.NoError(t, err)

	// Can run PTP config change handler without errors
	p, d := E810("e810")
	err = p.OnPTPConfigChange(d, profile)
	assert.NoError(t, err)
	ccData := clockChain.(*ClockChain)
	assert.Equal(t, ClockTypeTBC, ccData.Type, "identified a wrong clock type")
	assert.Equal(t, "5799633565432596414", ccData.LeadingNIC.DpllClockID, "identified a wrong clock ID ")
	assert.Equal(t, 9, len(ccData.LeadingNIC.Pins), "wrong number of configurable pins")
	assert.Equal(t, "ens4f1", ccData.LeadingNIC.UpstreamPort, "wrong upstream port")
	// Note: Intentionally NOT calling mockFS.VerifyAllCalls(t) because the mockFS is set up permissibly, not for enforcing
}

func Test_ProcessProfileTGMOld(t *testing.T) {
	unitTest = true
	profile, err := loadProfile("./testdata/profile-tgm-old.yaml")
	assert.NoError(t, err)
	p, d := E810("e810")
	err = p.OnPTPConfigChange(d, profile)
	assert.NoError(t, err)
}

func TestEnableE810Outputs(t *testing.T) {
	tests := []struct {
		name          string
		setupMock     func(*MockFileSystem)
		clockChain    *ClockChain
		expectedError string
	}{
		{
			name: "Successful execution - single PHC",
			clockChain: &ClockChain{
				LeadingNIC: CardInfo{Name: "ens4f0"},
			},
			setupMock: func(m *MockFileSystem) {
				phcEntries := []os.DirEntry{
					MockDirEntry{name: "ptp0", isDir: true},
				}
				m.ExpectReadDir("/sys/class/net/ens4f0/device/ptp/", phcEntries, nil)
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA2", []byte("2 2"), os.FileMode(0o666), nil)
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/period", []byte("2 0 0 1 0"), os.FileMode(0o666), nil)
			},
			expectedError: "",
		},
		{
			name: "ReadDir fails",
			clockChain: &ClockChain{
				LeadingNIC: CardInfo{Name: "ens4f0"},
			},
			setupMock: func(m *MockFileSystem) {
				m.ExpectReadDir("/sys/class/net/ens4f0/device/ptp/", []os.DirEntry{}, errors.New("permission denied"))
			},
			expectedError: "e810 failed to read /sys/class/net/ens4f0/device/ptp/: permission denied",
		},
		{
			name: "No PHC directories found",
			clockChain: &ClockChain{
				LeadingNIC: CardInfo{Name: "ens4f0"},
			},
			setupMock: func(m *MockFileSystem) {
				m.ExpectReadDir("/sys/class/net/ens4f0/device/ptp/", []os.DirEntry{}, nil)
			},
			expectedError: "e810 cards should have one PHC per NIC, but ens4f0 has 0",
		},
		{
			name: "Multiple PHC directories found (warning case)",
			clockChain: &ClockChain{
				LeadingNIC: CardInfo{Name: "ens4f0"},
			},
			setupMock: func(m *MockFileSystem) {
				phcEntries := []os.DirEntry{
					MockDirEntry{name: "ptp0", isDir: true},
					MockDirEntry{name: "ptp1", isDir: true},
				}
				m.ExpectReadDir("/sys/class/net/ens4f0/device/ptp/", phcEntries, nil)
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA2", []byte("2 2"), os.FileMode(0o666), nil)
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/period", []byte("2 0 0 1 0"), os.FileMode(0o666), nil)
			},
			expectedError: "",
		},
		{
			name: "SMA2 write fails",
			clockChain: &ClockChain{
				LeadingNIC: CardInfo{Name: "ens4f0"},
			},
			setupMock: func(m *MockFileSystem) {
				phcEntries := []os.DirEntry{
					MockDirEntry{name: "ptp0", isDir: true},
				}
				m.ExpectReadDir("/sys/class/net/ens4f0/device/ptp/", phcEntries, nil)
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA2", []byte("2 2"), os.FileMode(0o666), errors.New("write failed"))
			},
			expectedError: "e810 failed to write 2 2 to /sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA2: write failed",
		},
		{
			name: "Period write fails - should not return error but log",
			clockChain: &ClockChain{
				LeadingNIC: CardInfo{Name: "ens4f0"},
			},
			setupMock: func(m *MockFileSystem) {
				phcEntries := []os.DirEntry{
					MockDirEntry{name: "ptp0", isDir: true},
				}
				m.ExpectReadDir("/sys/class/net/ens4f0/device/ptp/", phcEntries, nil)
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA2", []byte("2 2"), os.FileMode(0o666), nil)
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/period", []byte("2 0 0 1 0"), os.FileMode(0o666), errors.New("period write failed"))
			},
			expectedError: "", // Function doesn't return error for period write failure
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mock filesystem
			mockFS, restore := setupMockFS()
			defer restore()
			tt.setupMock(mockFS)

			// Execute function
			err := tt.clockChain.EnableE810Outputs()

			// Check error
			if tt.expectedError != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
			} else {
				assert.NoError(t, err)
			}

			// Verify all expected calls were made
			mockFS.VerifyAllCalls(t)
		})
	}
}

func Test_AfterRunPTPCommandE810(t *testing.T) {
	unitTest = true
	profile, err := loadProfile("./testdata/profile-tgm.yaml")
	assert.NoError(t, err)
	p, d := E810("e810")

	err = p.AfterRunPTPCommand(d, profile, "bad command")
	assert.NoError(t, err)

	mClockChain := &mockClockChain{}
	clockChain = mClockChain
	err = p.AfterRunPTPCommand(d, profile, "reset-to-default")
	assert.NoError(t, err)
	mClockChain.assertCallCounts(t, 0, 0, 1)

	mClockChain.returnErr = fmt.Errorf("Fake error")
	err = p.AfterRunPTPCommand(d, profile, "reset-to-default")
	assert.Error(t, err)
	mClockChain.assertCallCounts(t, 0, 0, 2)

	mClockChain = &mockClockChain{}
	clockChain = mClockChain
	err = p.AfterRunPTPCommand(d, profile, "tbc-ho-entry")
	assert.NoError(t, err)
	mClockChain.assertCallCounts(t, 0, 1, 0)
	mClockChain.returnErr = fmt.Errorf("Fake error")
	err = p.AfterRunPTPCommand(d, profile, "tbc-ho-entry")
	assert.Error(t, err)
	mClockChain.assertCallCounts(t, 0, 2, 0)

	mClockChain = &mockClockChain{}
	clockChain = mClockChain
	err = p.AfterRunPTPCommand(d, profile, "tbc-ho-exit")
	assert.NoError(t, err)
	mClockChain.assertCallCounts(t, 1, 0, 0)
	mClockChain.returnErr = fmt.Errorf("Fake error")
	err = p.AfterRunPTPCommand(d, profile, "tbc-ho-exit")
	assert.Error(t, err)
	mClockChain.assertCallCounts(t, 2, 0, 0)
}

func Test_PopulateHwConfdigE810(t *testing.T) {
	p, d := E810("e810")
	data := (*d).(*E810PluginData)
	err := p.PopulateHwConfig(d, nil)
	assert.NoError(t, err)

	output := []ptpv1.HwConfig{}
	err = p.PopulateHwConfig(d, &output)
	assert.NoError(t, err)
	assert.Equal(t, 0, len(output))

	data.hwplugins = &[]string{"A", "B", "C"}
	err = p.PopulateHwConfig(d, &output)
	assert.NoError(t, err)
	assert.Equal(t, []ptpv1.HwConfig{
		{
			DeviceID: "e810",
			Status:   "A",
		},
		{
			DeviceID: "e810",
			Status:   "B",
		},
		{
			DeviceID: "e810",
			Status:   "C",
		},
	},
		output)
}
