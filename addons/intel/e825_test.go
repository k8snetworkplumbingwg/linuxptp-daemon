package intel

import (
	"fmt"
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
	profile, err := loadProfile("./testdata/e825-tgm.yaml")
	assert.NoError(t, err)
	p, d := E825("e825")

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
