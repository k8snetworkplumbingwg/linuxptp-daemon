package ptp4lconf

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConf_Populate_PortRoleSummary(t *testing.T) {
	tests := []struct {
		name          string
		config        string
		expectedRoles PortRoleSummary
	}{
		{
			name:   "global slaveOnly 1",
			config: "[global]\nslaveOnly 1\n",
			expectedRoles: PortRoleSummary{
				SlaveOnlyTrue: 1,
			},
		},
		{
			name:   "global clientOnly 1",
			config: "[global]\nclientOnly 1\n",
			expectedRoles: PortRoleSummary{
				SlaveOnlyTrue: 1,
			},
		},
		{
			name:   "single port with masterOnly 1",
			config: "[global]\n[ens1f0]\nmasterOnly 1\n",
			expectedRoles: PortRoleSummary{
				MasterOnlyTrue:   1,
				SlaveOnlyDefault: 1,
				TotalPorts:       1,
			},
		},
		{
			name:   "single port with masterOnly 0",
			config: "[global]\n[ens1f0]\nmasterOnly 0\n",
			expectedRoles: PortRoleSummary{
				MasterOnlyFalse:  1,
				SlaveOnlyDefault: 1,
				TotalPorts:       1,
			},
		},
		{
			name:   "single port with clientOnly 1",
			config: "[global]\n[ens1f0]\nclientOnly 1\n",
			expectedRoles: PortRoleSummary{
				SlaveOnlyTrue:     1,
				MasterOnlyDefault: 1,
				TotalPorts:        1,
			},
		},
		{
			name:   "two ports all masterOnly 1",
			config: "[global]\n[ens1f0]\nmasterOnly 1\n[ens1f1]\nmasterOnly 1",
			expectedRoles: PortRoleSummary{
				MasterOnlyTrue:   2,
				SlaveOnlyDefault: 2,
				TotalPorts:       2,
			},
		},
		{
			name:   "two ports mixed masterOnly",
			config: "[global]\n[ens1f0]\nmasterOnly 0\n[ens1f1]\nmasterOnly 1",
			expectedRoles: PortRoleSummary{
				MasterOnlyTrue:   1,
				MasterOnlyFalse:  1,
				SlaveOnlyDefault: 2,
				TotalPorts:       2,
			},
		},
		{
			name:   "serverOnly 0 and masterOnly 1",
			config: "[global]\n[ens1f0]\nserverOnly 0\n[ens1f1]\nmasterOnly 1\n",
			expectedRoles: PortRoleSummary{
				MasterOnlyTrue:   1,
				MasterOnlyFalse:  1,
				SlaveOnlyDefault: 2,
				TotalPorts:       2,
			},
		},
		{
			name:   "two ports no role settings",
			config: "[global]\n[ens1f0]\n[ens1f1]\n",
			expectedRoles: PortRoleSummary{
				MasterOnlyDefault: 2,
				SlaveOnlyDefault:  2,
				TotalPorts:        2,
			},
		},
		{
			name:          "no ports",
			config:        "[global]\ndomainNumber 24\n",
			expectedRoles: PortRoleSummary{},
		},
		{
			name:   "port with slaveOnly 0",
			config: "[global]\n[ens1f0]\nslaveOnly 0\n",
			expectedRoles: PortRoleSummary{
				SlaveOnlyFalse:    1,
				MasterOnlyDefault: 1,
				TotalPorts:        1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conf := &Conf{}
			err := conf.Populate(&tt.config)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedRoles, conf.PortRoles,
				"PortRoleSummary mismatch")
		})
	}
}

func TestConf_Populate_Parsing(t *testing.T) {
	config := "[global]\ndomainNumber 24\n[ens1f0]\nmasterOnly 1\n[ens1f1]\nmasterOnly 0"
	conf := &Conf{}
	err := conf.Populate(&config)
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
	err := conf.Populate(&badConfig)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Section missing closing ']'")
}

func TestConf_Populate_NilConfig(t *testing.T) {
	conf := &Conf{}
	err := conf.Populate(nil)
	assert.NoError(t, err)
	assert.Equal(t, PortRoleSummary{}, conf.PortRoles)
}

func TestConf_Populate_SkipsComments(t *testing.T) {
	config := "[global]\n# this is a comment\ndomainNumber 24"
	conf := &Conf{}
	err := conf.Populate(&config)
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
