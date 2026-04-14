package ptp4lconf

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/stretchr/testify/assert"
)

func stringPtr(s string) *string { return &s }

func TestConf_Populate_ClockType(t *testing.T) {
	tests := []struct {
		name              string
		config            string
		cliArgs           *string
		expectedClockType event.ClockType
	}{
		{
			name:              "OC with -s flag",
			config:            "[global]\n",
			cliArgs:           stringPtr("-s -f /etc/ptp4l.conf"),
			expectedClockType: event.OC,
		},
		{
			name:              "OC with -s and single interface",
			config:            "[global]\n[ens1f0]\n",
			cliArgs:           stringPtr("-s"),
			expectedClockType: event.OC,
		},
		{
			name:              "BC with -s and multiple interfaces",
			config:            "[global]\n[ens1f0]\nmasterOnly 0\n[ens1f1]\nmasterOnly 1",
			cliArgs:           stringPtr("-s"),
			expectedClockType: event.BC,
		},
		{
			name:              "GM with masterOnly interfaces",
			config:            "[global]\n[ens1f0]\nmasterOnly 1\n[ens1f1]\nmasterOnly 1",
			cliArgs:           stringPtr("-f /etc/ptp4l.conf"),
			expectedClockType: event.GM,
		},
		{
			name:              "OC with slaveOnly config",
			config:            "[global]\nslaveOnly 1\n",
			cliArgs:           nil,
			expectedClockType: event.OC,
		},
		{
			name:              "GM with masterOnly config",
			config:            "[global]\n[ens1f0]\nmasterOnly 1\n",
			cliArgs:           nil,
			expectedClockType: event.GM,
		},
		{
			name:              "BC with serverOnly 0",
			config:            "[global]\n[ens1f0]\nserverOnly 0\n[ens1f1]\nmasterOnly 1\n",
			cliArgs:           nil,
			expectedClockType: event.BC,
		},
		{
			name:              "OC with clientOnly 1",
			config:            "[global]\n[ens1f0]\nclientOnly 1\n",
			cliArgs:           nil,
			expectedClockType: event.OC,
		},
		{
			name:              "OC with --slaveOnly 1",
			config:            "[global]\n",
			cliArgs:           stringPtr("--slaveOnly 1 -f /etc/ptp4l.conf"),
			expectedClockType: event.OC,
		},
		{
			name:              "OC with --slaveOnly=1",
			config:            "[global]\n",
			cliArgs:           stringPtr("--slaveOnly=1 -f /etc/ptp4l.conf"),
			expectedClockType: event.OC,
		},
		{
			name:              "GM with --slaveOnly 0",
			config:            "[global]\n[ens1f0]\n",
			cliArgs:           stringPtr("--slaveOnly 0 -f /etc/ptp4l.conf"),
			expectedClockType: event.GM,
		},
		{
			name:              "OC with --clientOnly 1",
			config:            "[global]\n",
			cliArgs:           stringPtr("--clientOnly 1 -f /etc/ptp4l.conf"),
			expectedClockType: event.OC,
		},
		{
			name:              "OC with --clientOnly=1",
			config:            "[global]\n",
			cliArgs:           stringPtr("--clientOnly=1 -f /etc/ptp4l.conf"),
			expectedClockType: event.OC,
		},
		{
			name:              "GM with --clientOnly 0",
			config:            "[global]\n[ens1f0]\n",
			cliArgs:           stringPtr("--clientOnly 0 -f /etc/ptp4l.conf"),
			expectedClockType: event.GM,
		},
		{
			name:              "GM with no slave configuration",
			config:            "[global]\n[ens1f0]\n[ens1f1]\n",
			cliArgs:           stringPtr("-f /etc/ptp4l.conf -m"),
			expectedClockType: event.GM,
		},
		{
			name:              "OC with -s at end of args",
			config:            "[global]\n",
			cliArgs:           stringPtr("-f /etc/ptp4l.conf -s"),
			expectedClockType: event.OC,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conf := &Conf{}
			err := conf.Populate(&tt.config, tt.cliArgs)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedClockType, conf.ClockType,
				"Clock type mismatch: expected %v, got %v", tt.expectedClockType, conf.ClockType)
		})
	}
}

func TestConf_Populate_Parsing(t *testing.T) {
	config := "[global]\ndomainNumber 24\n[ens1f0]\nmasterOnly 1\n[ens1f1]\nmasterOnly 0"
	conf := &Conf{}
	err := conf.Populate(&config, nil)
	assert.NoError(t, err)
	assert.Len(t, conf.Sections, 3)

	val, ok := conf.GetOption("[global]", "domainNumber")
	assert.True(t, ok)
	assert.Equal(t, "24", val)

	val, ok = conf.GetOption("[ens1f0]", "masterOnly")
	assert.True(t, ok)
	assert.Equal(t, "1", val)

	val, ok = conf.GetOption("[ens1f1]", "masterOnly")
	assert.True(t, ok)
	assert.Equal(t, "0", val)
}

func TestConf_SetOption(t *testing.T) {
	conf := &Conf{}
	conf.Sections = make([]Section, 0)

	conf.SetOption("[global]", "domainNumber", "24", false)
	val, ok := conf.GetOption("[global]", "domainNumber")
	assert.True(t, ok)
	assert.Equal(t, "24", val)

	conf.SetOption("[global]", "domainNumber", "48", true)
	val, ok = conf.GetOption("[global]", "domainNumber")
	assert.True(t, ok)
	assert.Equal(t, "48", val)

	conf.SetOption("[global]", "domainNumber", "99", false)
	val, ok = conf.GetOption("[global]", "domainNumber")
	assert.True(t, ok)
	assert.Equal(t, "48", val, "without overwrite, first match should be returned")
}

func TestConf_GetOption_NotFound(t *testing.T) {
	conf := &Conf{}
	conf.Sections = make([]Section, 0)
	conf.SetOption("[global]", "domainNumber", "24", false)

	_, ok := conf.GetOption("[global]", "nonexistent")
	assert.False(t, ok)

	_, ok = conf.GetOption("[nonexistent]", "domainNumber")
	assert.False(t, ok)
}

func TestConf_Populate_Error(t *testing.T) {
	badConfig := "[global\ndomainNumber 24"
	conf := &Conf{}
	err := conf.Populate(&badConfig, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Section missing closing ']'")
}

func TestConf_Populate_NilConfig(t *testing.T) {
	conf := &Conf{}
	err := conf.Populate(nil, nil)
	assert.NoError(t, err)
	assert.Equal(t, event.GM, conf.ClockType)
}

func TestConf_Populate_SkipsComments(t *testing.T) {
	config := "[global]\n# this is a comment\ndomainNumber 24"
	conf := &Conf{}
	err := conf.Populate(&config, nil)
	assert.NoError(t, err)

	val, ok := conf.GetOption("[global]", "domainNumber")
	assert.True(t, ok)
	assert.Equal(t, "24", val)
}

func TestSection_Name(t *testing.T) {
	assert.Equal(t, "ens1f0", Section{SectionName: "[ens1f0]"}.Name())
	assert.Equal(t, "global", Section{SectionName: "[global]"}.Name())
	assert.Equal(t, "", Section{SectionName: "[]"}.Name())
}
