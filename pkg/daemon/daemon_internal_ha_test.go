package daemon

import (
	"strings"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/testhelpers"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

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
	assert.NotNil(t, phc2sysProc, "phc2sys process should exist")

	// Initialize haProfile map (normally done in NewPhc2sysProcess)
	phc2sysProc.haProfile = make(map[string][]string)

	// Create OSClockConfigs to simulate the HA configuration
	osClockConfigs := OSClockConfigs{
		haProfileNames:       []string{"ha_profile1"},
		haReferencedProfiles: []string{"profile1", "profile2"},
		profileRunID:         map[string]int{"profile1": 0, "profile2": 1},
	}

	// Create daemon and call populateHAInterfaces
	dd := NewDaemonForTests(&ReadyTracker{}, processManager)
	dd.populateHAInterfaces(&osClockConfigs)

	// Verify that haProfile was populated correctly
	assert.NotNil(t, phc2sysProc.haProfile, "haProfile should not be nil")
	assert.Equal(t, 2, len(phc2sysProc.haProfile), "haProfile should have 2 entries")
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

	// Create OSClockConfigs to simulate HA configuration with two ptp4l profiles
	osClockConfigs := OSClockConfigs{
		haProfileNames:       []string{"ha_profile1"},
		haReferencedProfiles: []string{"profile1", "profile2"},
		profileRunID:         map[string]int{"profile1": 0, "profile2": 1},
	}

	// Call haSocketOpts which should generate socket options for both referenced profiles
	socketOpts := haSocketOpts(&osClockConfigs)

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

			UpdateProcessStatusMetrics(phc2sysProcessName, tt.metricCfgName, PtpProcessUp)
			assert.Equal(t, 1, testutil.CollectAndCount(ProcessStatus))
			assert.Equal(t, 1, testutil.CollectAndCount(ProcessRestartCount))

			deleteMetrics(nil, nil, phc2sysProcessName, "phc2sys.1.config", "[ptp4l.1.config:{level}]")

			assert.Equal(t, 0, testutil.CollectAndCount(ProcessStatus))
			assert.Equal(t, 0, testutil.CollectAndCount(ProcessRestartCount))
		})
	}
}
