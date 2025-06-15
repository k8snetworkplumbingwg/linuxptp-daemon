package daemon

// This tests daemon private functions

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bigkevmcd/go-configparser"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/yaml"
)

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
		false,
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
	err := dn.applyNodePtpProfile(0, profile)
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
	testDataFiles := []string{
		"testdata/profile-tbc-tt.yaml",
		"testdata/profile-tbc-tr.yaml",
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
		false,
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

	for i := range len(testDataFiles) {
		mkPath(t)
		profile, err := loadProfile(testDataFiles[i])
		assert.NoError(t, err)
		// Will assert inside in case of error:
		err = dn.applyNodePtpProfile(0, profile)
		assert.NoError(t, err)
		clean(t)
	}
}

func TestGetPTPClockId_ValidInput(t *testing.T) {
	p := &ptpProcess{
		nodeProfile: ptpv1.PtpProfile{
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
		nodeProfile: ptpv1.PtpProfile{
			PtpSettings: map[string]string{},
		},
	}

	_, err := p.getPTPClockID()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "leadingInterface not found in ptpProfile")
}

func TestGetPTPClockId_MissingClockId(t *testing.T) {
	p := &ptpProcess{
		nodeProfile: ptpv1.PtpProfile{
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
		nodeProfile: ptpv1.PtpProfile{
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
