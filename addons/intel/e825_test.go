package intel

import (
	"slices"
	"testing"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
)

func Test_E825(t *testing.T) {
	p, d := E825("e825")
	assert.NotNil(t, p)
	assert.NotNil(t, d)

	p, d = E825("not_e825")
	assert.Nil(t, p)
	assert.Nil(t, d)
}

func Test_AfterRunPTPCommandE825(t *testing.T) {
	unitTest = true

	defaultUblxCmds := []string{
		"CFG-MSG,1,34,1",
		"CFG-MSG,1,3,1",
		"CFG-MSG,0xf0,0x02,0",
		"CFG-MSG,0xf0,0x03,0",
		"CFG-MSGOUT-NMEA_ID_VTG_USB,0",
		"CFG-MSGOUT-NMEA_ID_GST_USB,0",
		"CFG-MSGOUT-NMEA_ID_ZDA_USB,0",
		"CFG-MSGOUT-NMEA_ID_GBS_USB,0",
		"SAVE",
	}

	testCases := []struct {
		name                   string
		profilePath            string
		expectedGnssCmd        string
		expectedHwPluginsCount int
	}{
		{
			name:                   "GNSS Disabled",
			profilePath:            "./testdata/e825-gnss-disabled.yaml",
			expectedGnssCmd:        "CFG-NAVSPG-INFIL_NCNOTHRS,50,1",
			expectedHwPluginsCount: 0,
		},
		{
			name:                   "GNSS Enabled",
			profilePath:            "./testdata/e825-gnss-enabled.yaml",
			expectedGnssCmd:        "CFG-NAVSPG-INFIL_NCNOTHRS,0,1",
			expectedHwPluginsCount: 0,
		},
		{
			name:                   "GNSS missing (default to enabled)",
			profilePath:            "./testdata/e825-tgm.yaml",
			expectedGnssCmd:        "CFG-NAVSPG-INFIL_NCNOTHRS,0,1",
			expectedHwPluginsCount: 3,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			profile, err := loadProfile(tc.profilePath)
			assert.NoError(t, err)

			p, d := E825("e825")
			data := (*d).(*E825PluginData)
			assert.NotNil(t, p)
			assert.NotNil(t, d)

			mockExec, execRestore := setupExecMock()
			defer execRestore()
			mockExec.setDefaults("output", nil)

			err = p.AfterRunPTPCommand(d, profile, "gpspipe")
			assert.NoError(t, err)

			requiredUblxCmds := append([]string{tc.expectedGnssCmd}, defaultUblxCmds...)
			found := make([]string, 0, len(requiredUblxCmds))
			for _, call := range mockExec.actualCalls {
				for _, arg := range call.args {
					if slices.Contains(requiredUblxCmds, arg) {
						found = append(found, arg)
					}
				}
			}
			// Check for all required default commands in order
			assert.Equal(t, requiredUblxCmds, found)

			// Check for the number of reported outputs
			assert.Equal(t, tc.expectedHwPluginsCount, len(*data.hwplugins), "Unexpected number of hwplugins")
		})
	}
}

func Test_PopulateHwConfdigE825(t *testing.T) {
	p, d := E825("e825")
	data := (*d).(*E825PluginData)
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
			DeviceID: "e825",
			Status:   "A",
		},
		{
			DeviceID: "e825",
			Status:   "B",
		},
		{
			DeviceID: "e825",
			Status:   "C",
		},
	},
		output)
}
