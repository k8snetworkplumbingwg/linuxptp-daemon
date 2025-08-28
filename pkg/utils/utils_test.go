package utils_test

import (
	"os"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/testhelpers"
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

// func Test_GetAlias_ByPhcID(t *testing.T) {
// 	am := &utils.AliasManager{}
// 	am.Populate(map[string][]string{
// 		"ptp0": {"ens1f1", "ens1f0"}, // ensure sorting within alias
// 		"ptp1": {"ens2f0"},
// 	})

// 	testCases := []testCase{
// 		{ifname: "ens1f0", expectedAlias: "ens1f0_ens1f1"},
// 		{ifname: "ens1f1", expectedAlias: "ens1f0_ens1f1"},
// 		{ifname: "ens2f0", expectedAlias: "ens2f0"},
// 		{ifname: "", expectedAlias: ""},                       // empty string should return empty string
// 		{ifname: "unknown-phc", expectedAlias: "unknown-phc"}, // unknown phc should return unknown-phc
// 	}
// 	for _, tc := range testCases {
// 		t.Run(tc.ifname, func(t *testing.T) {
// 			assert.Equal(t, tc.expectedAlias, am.GetAlias(tc.ifname))
// 		})
// 	}
// }
