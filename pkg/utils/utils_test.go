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

func Test_GetAlias_ByPhcID(t *testing.T) {
	utils.FileSystem = &utils.MockedReadlinkFS{ReadLinkValues: map[string]string{"ens1f0": "./0000.ac.000.0", "ens1f1": "./0000.ac.000.1"}}
	am := &utils.AliasManager{}
	am.PopulateBusIDs("ens1f0", "ens1f1")

	testCases := []testCase{
		{ifname: "ens1f0", expectedAlias: "0000.ac.000.x"},
		{ifname: "ens1f1", expectedAlias: "0000.ac.000.x"},
		{ifname: "ens2f0", expectedAlias: "ens2f0"},
		{ifname: "", expectedAlias: ""},                       // empty string should return empty string
		{ifname: "unknown-phc", expectedAlias: "unknown-phc"}, // unknown phc should return unknown-phc
	}
	for _, tc := range testCases {
		t.Run("test-"+tc.ifname, func(t *testing.T) {
			assert.Equal(t, tc.expectedAlias, am.GetAlias(tc.ifname))
		})
	}
}
