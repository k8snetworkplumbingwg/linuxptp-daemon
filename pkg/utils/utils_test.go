package utils_test

import (
	"os"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/testhelpers"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/stretchr/testify/assert"
)

func TestMain(m *testing.M) {
	teardownTests := testhelpers.SetupTests()
	defer teardownTests()
	os.Exit(m.Run())
}

type testCase struct {
	ifname        string
	expectedAlias string
}

// testIfaceCollection implements utils.iFaceCollection for testing
type testIfaceCollection struct {
	phcToIfnames map[string][]string
}

func (c testIfaceCollection) GetIfNamesGroupedByPhc() map[string][]string {
	return c.phcToIfnames
}

func Test_GetAlias_ByPhcID(t *testing.T) {
	am := &utils.AliasManager{}
	am.Populate(testIfaceCollection{phcToIfnames: map[string][]string{
		"ptp0": {"ens1f1", "ens1f0"}, // ensure sorting within alias
		"ptp1": {"ens2f0"},
	}})

	testCases := []testCase{
		{ifname: "ptp0", expectedAlias: "ens1f0_ens1f1"},
		{ifname: "ptp1", expectedAlias: "ens2f0"},
		{ifname: "", expectedAlias: ""},                       // empty string should return empty string
		{ifname: "unknown-phc", expectedAlias: "unknown-phc"}, // unknown phc should return unknown-phc
	}
	for _, tc := range testCases {
		t.Run(tc.ifname, func(t *testing.T) {
			assert.Equal(t, tc.expectedAlias, am.GetAlias(tc.ifname))
		})
	}
}
