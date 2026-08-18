package pmc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	portDataSetResponse = "\tb4e9b8.fffe.5ec71a-0 seq 1 RESPONSE MANAGEMENT PORT_DATA_SET \n" +
		"\t\tportIdentity            b4e9b8.fffe.5ec71a-1\n" +
		"\t\tportState               SLAVE\n" +
		"\t\tlogMinDelayReqInterval  0\n" +
		"\t\tpeerMeanPathDelay       0\n" +
		"\t\tlogAnnounceInterval     1\n" +
		"\t\tannounceReceiptTimeout  3\n" +
		"\t\tlogSyncInterval         0\n" +
		"\t\tdelayMechanism          1\n" +
		"\t\tlogMinPdelayReqInterval 0\n" +
		"\t\tversionNumber           2\n"

	parentDataSetResponse = "\tb4e9b8.fffe.5ec71a-0 seq 0 RESPONSE MANAGEMENT PARENT_DATA_SET \n" +
		"\t\tparentPortIdentity                    b4e9b8.fffe.5ec71a-0\n" +
		"\t\tparentStats                           0\n" +
		"\t\tobservedParentOffsetScaledLogVariance 0xffff\n" +
		"\t\tobservedParentClockPhaseChangeRate    0x7fffffff\n" +
		"\t\tgrandmasterPriority1                  128\n" +
		"\t\tgm.ClockClass                         6\n" +
		"\t\tgm.ClockAccuracy                      0x21\n" +
		"\t\tgm.OffsetScaledLogVariance            0x4e5d\n" +
		"\t\tgrandmasterPriority2                  128\n" +
		"\t\tgrandmasterIdentity                   507c6f.fffe.1fb16c\n"
)

func TestGetMonitorRegex_ParentOnly(t *testing.T) {
	re := GetMonitorRegex(true, false)

	assert.NotNil(t, re.FindStringSubmatch(parentDataSetResponse),
		"should match PARENT_DATA_SET response")
	assert.Nil(t, re.FindStringSubmatch(portDataSetResponse),
		"should NOT match PORT_DATA_SET when monitorPortState=false")
}

func TestGetMonitorRegex_PortOnly(t *testing.T) {
	re := GetMonitorRegex(false, true)

	assert.NotNil(t, re.FindStringSubmatch(portDataSetResponse),
		"should match PORT_DATA_SET response")
	assert.Nil(t, re.FindStringSubmatch(parentDataSetResponse),
		"should NOT match PARENT_DATA_SET when monitorParentData=false")
}

func TestGetMonitorRegex_Both(t *testing.T) {
	re := GetMonitorRegex(true, true)

	assert.NotNil(t, re.FindStringSubmatch(parentDataSetResponse),
		"should match PARENT_DATA_SET when both are enabled")
	assert.NotNil(t, re.FindStringSubmatch(portDataSetResponse),
		"should match PORT_DATA_SET when both are enabled")
}

func TestGetMonitorRegex_AlternationBug_RequiresResponsePrefix(t *testing.T) {
	re := GetMonitorRegex(true, true)

	noPrefixPort := "\t\tportIdentity            b4e9b8.fffe.5ec71a-1\n" +
		"\t\tportState               SLAVE\n" +
		"\t\tlogMinDelayReqInterval  0\n" +
		"\t\tpeerMeanPathDelay       0\n" +
		"\t\tlogAnnounceInterval     1\n" +
		"\t\tannounceReceiptTimeout  3\n" +
		"\t\tlogSyncInterval         0\n" +
		"\t\tdelayMechanism          1\n" +
		"\t\tlogMinPdelayReqInterval 0\n" +
		"\t\tversionNumber           2\n"

	assert.Nil(t, re.FindStringSubmatch(noPrefixPort),
		"PORT_DATA_SET body without RESPONSE MANAGEMENT prefix must NOT match")
}

func TestGetMonitorRegex_DoesNotMatchPortDataSetNP(t *testing.T) {
	re := GetMonitorRegex(false, true)

	portDataSetNPResponse := "\tb4e9b8.fffe.5ec71a-0 seq 1 RESPONSE MANAGEMENT PORT_DATA_SET_NP \n" +
		"\t\tneighborPropDelayThresh 0\n" +
		"\t\tasCapable               1\n"

	assert.Nil(t, re.FindStringSubmatch(portDataSetNPResponse),
		"PORT_DATA_SET_NP must NOT match the PORT_DATA_SET regex")
}

func TestPortDataSetRegExp_MatchesRealResponse(t *testing.T) {
	re := PortDataSetRegExp()
	require.NotNil(t, re)

	matches := re.FindStringSubmatch(portDataSetResponse)
	require.NotNil(t, matches)
	assert.Equal(t, "b4e9b8.fffe.5ec71a-1", matches[1])
	assert.Equal(t, "SLAVE", matches[2])
}
