package protocol

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testPortIdentity = "b4e9b8.fffe.5ec71a-1"

func TestPortDataSet_RegEx_MatchesRealPmcResponse(t *testing.T) {
	re := regexp.MustCompile((&PortDataSet{}).RegEx())

	response := "\tb4e9b8.fffe.5ec71a-0 seq 0 RESPONSE MANAGEMENT PORT_DATA_SET \n" +
		"\t\tportIdentity            " + testPortIdentity + "\n" +
		"\t\tportState               SLAVE\n" +
		"\t\tlogMinDelayReqInterval  0\n" +
		"\t\tpeerMeanPathDelay       0\n" +
		"\t\tlogAnnounceInterval     1\n" +
		"\t\tannounceReceiptTimeout  3\n" +
		"\t\tlogSyncInterval         0\n" +
		"\t\tdelayMechanism          1\n" +
		"\t\tlogMinPdelayReqInterval 0\n" +
		"\t\tversionNumber           2\n"

	matches := re.FindStringSubmatch(response)
	require.NotNil(t, matches, "regex should match a real pmc RESPONSE MANAGEMENT PORT_DATA_SET body")

	pds, err := ProcessMessage[PortDataSet](matches)
	require.NoError(t, err)

	assert.Equal(t, testPortIdentity, pds.PortIdentity)
	assert.Equal(t, "SLAVE", pds.PortState)
	assert.Equal(t, int8(0), pds.LogMinDelayReqInterval)
	assert.Equal(t, int64(0), pds.PeerMeanPathDelay)
	assert.Equal(t, int8(1), pds.LogAnnounceInterval)
	assert.Equal(t, uint8(3), pds.AnnounceReceiptTimeout)
	assert.Equal(t, int8(0), pds.LogSyncInterval)
	assert.Equal(t, uint8(1), pds.DelayMechanism)
	assert.Equal(t, int8(0), pds.LogMinPdelayReqInterval)
	assert.Equal(t, uint8(2), pds.VersionNumber)
}

func TestPortDataSet_RegEx_MatchesNegativeValues(t *testing.T) {
	re := regexp.MustCompile((&PortDataSet{}).RegEx())

	response := "\t\tportIdentity            c45ab1.ffff.545c05-2\n" +
		"\t\tportState               MASTER\n" +
		"\t\tlogMinDelayReqInterval  -3\n" +
		"\t\tpeerMeanPathDelay       -1234\n" +
		"\t\tlogAnnounceInterval     -1\n" +
		"\t\tannounceReceiptTimeout  3\n" +
		"\t\tlogSyncInterval         -4\n" +
		"\t\tdelayMechanism          2\n" +
		"\t\tlogMinPdelayReqInterval -2\n" +
		"\t\tversionNumber           2\n"

	matches := re.FindStringSubmatch(response)
	require.NotNil(t, matches)

	pds, err := ProcessMessage[PortDataSet](matches)
	require.NoError(t, err)

	assert.Equal(t, "c45ab1.ffff.545c05-2", pds.PortIdentity)
	assert.Equal(t, "MASTER", pds.PortState)
	assert.Equal(t, int8(-3), pds.LogMinDelayReqInterval)
	assert.Equal(t, int64(-1234), pds.PeerMeanPathDelay)
	assert.Equal(t, int8(-1), pds.LogAnnounceInterval)
	assert.Equal(t, int8(-4), pds.LogSyncInterval)
	assert.Equal(t, int8(-2), pds.LogMinPdelayReqInterval)
}

func TestPortDataSet_RegEx_AllPortStates(t *testing.T) {
	re := regexp.MustCompile((&PortDataSet{}).RegEx())

	states := []string{
		"NONE", "INITIALIZING", "FAULTY", "DISABLED", "LISTENING",
		"PRE_MASTER", "MASTER", "PASSIVE", "UNCALIBRATED", "SLAVE",
		"GRAND_MASTER",
	}
	for _, state := range states {
		input := "\t\tportIdentity            " + testPortIdentity + "\n" +
			"\t\tportState               " + state + "\n" +
			"\t\tlogMinDelayReqInterval  0\n" +
			"\t\tpeerMeanPathDelay       0\n" +
			"\t\tlogAnnounceInterval     1\n" +
			"\t\tannounceReceiptTimeout  3\n" +
			"\t\tlogSyncInterval         0\n" +
			"\t\tdelayMechanism          1\n" +
			"\t\tlogMinPdelayReqInterval 0\n" +
			"\t\tversionNumber           2\n"

		matches := re.FindStringSubmatch(input)
		assert.NotNilf(t, matches, "portState=%s should match", state)
	}
}

func TestPortDataSet_PortNumber(t *testing.T) {
	tests := []struct {
		name      string
		identity  string
		wantPort  int
		wantError bool
	}{
		{"port 1", testPortIdentity, 1, false},
		{"port 0", "b4e9b8.fffe.5ec71a-0", 0, false},
		{"port 21", "c45ab1.ffff.545c05-21", 21, false},
		{"no suffix", "b4e9b8.fffe.5ec71a", 0, true},
		{"trailing dash", "b4e9b8.fffe.5ec71a-", 0, true},
		{"non-numeric", "b4e9b8.fffe.5ec71a-abc", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pds := &PortDataSet{PortIdentity: tt.identity}
			got, err := pds.PortNumber()
			if tt.wantError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantPort, got)
			}
		})
	}
}

func TestPortDataSet_Equal(t *testing.T) {
	a := &PortDataSet{PortIdentity: testPortIdentity, PortState: "SLAVE"}
	b := &PortDataSet{PortIdentity: testPortIdentity, PortState: "SLAVE"}
	c := &PortDataSet{PortIdentity: testPortIdentity, PortState: "MASTER"}

	assert.True(t, a.Equal(b))
	assert.False(t, a.Equal(c))
	assert.True(t, (*PortDataSet)(nil).Equal(nil))
	assert.False(t, a.Equal(nil))
	assert.False(t, (*PortDataSet)(nil).Equal(a))
}

func TestPortDataSet_MonitorRegEx_CompilesWithoutCaptureGroups(t *testing.T) {
	pattern := (&PortDataSet{}).MonitorRegEx()
	re, err := regexp.Compile(pattern)
	require.NoError(t, err)
	assert.Equal(t, 0, re.NumSubexp(), "MonitorRegEx must not declare capture groups")
}

func TestPortDataSet_String_NilReceiver(t *testing.T) {
	var p *PortDataSet
	assert.Equal(t, "", p.String())
}
