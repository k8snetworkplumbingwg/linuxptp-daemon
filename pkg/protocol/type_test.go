package protocol

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real gmIdentity/stepsRemoved values captured from live pmc traffic against
// a T-BC (see the "SET EXTERNAL_GRANDMASTER_PROPERTIES_NP" / "RESPONSE
// MANAGEMENT EXTERNAL_GRANDMASTER_PROPERTIES_NP" lines in daemon logs).
// Every real clock identity is hex (EUI-64 style) and virtually always
// contains the literal "fffe" OUI/EUI-64 extension marker, which is pure
// hex letters - this is exactly what the old `\d*\.\d*\.\d*` pattern could
// never match.
const (
	realGMIdentityUpstream = "507c6f.fffe.1fb16c"
	realGMIdentityLocal    = "8faf00.fffe.cf0f3b"
)

func TestExternalGrandmasterProperties_RegEx_MatchesRealPmcResponse(t *testing.T) {
	re := regexp.MustCompile((&ExternalGrandmasterProperties{}).RegEx())

	tests := []struct {
		name             string
		input            string
		wantGMIdentity   string
		wantStepsRemoved string
	}{
		{
			name: "RESPONSE MANAGEMENT body (real capture)",
			input: "\tb4e9b8.fffe.5ec71a-0 seq 0 RESPONSE MANAGEMENT EXTERNAL_GRANDMASTER_PROPERTIES_NP \n" +
				"\t\tgmIdentity   507c6f.fffe.1fb16c\n" +
				"\t\tstepsRemoved 2\n",
			wantGMIdentity:   realGMIdentityUpstream,
			wantStepsRemoved: "2",
		},
		{
			name:             "outgoing SET command body built from String()",
			input:            " gmIdentity 8faf00.fffe.cf0f3b  stepsRemoved        0 \n",
			wantGMIdentity:   realGMIdentityLocal,
			wantStepsRemoved: "0",
		},
		{
			name:             "uppercase hex identity",
			input:            " gmIdentity 507C6F.FFFE.1FB16C  stepsRemoved        2 \n",
			wantGMIdentity:   "507C6F.FFFE.1FB16C",
			wantStepsRemoved: "2",
		},
		{
			name:             "all-zero identity",
			input:            " gmIdentity 000000.0000.000000  stepsRemoved        0 \n",
			wantGMIdentity:   "000000.0000.000000",
			wantStepsRemoved: "0",
		},
		{
			name:             "identity that is decimal-only (must still match: digits are valid hex)",
			input:            " gmIdentity 001122.334455.667788  stepsRemoved        1 \n",
			wantGMIdentity:   "001122.334455.667788",
			wantStepsRemoved: "1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := re.FindStringSubmatch(tt.input)
			if !assert.NotNilf(t, matches, "expected a match for input %q", tt.input) {
				return
			}
			require.Len(t, matches, 3, "expected 2 capture groups plus the full match")
			assert.Equal(t, tt.wantGMIdentity, matches[1], "gmIdentity capture group")
			assert.Equal(t, tt.wantStepsRemoved, matches[2], "stepsRemoved capture group")
		})
	}
}

// TestExternalGrandmasterProperties_RegEx_RejectsOldBuggyPattern locks in the
// regression: the old `\d*\.\d*\.\d*` gmIdentity pattern could never match a
// real (hex) clock identity, which made every SET/GET
// EXTERNAL_GRANDMASTER_PROPERTIES_NP call time out. This test fails if the
// pattern is ever reverted to a decimal-only shape.
func TestExternalGrandmasterProperties_RegEx_RejectsOldBuggyPattern(t *testing.T) {
	// Only gmIdentity's pattern changed; reuse the real stepsRemoved pattern
	// so this test isolates exactly the part of the fix under test.
	oldValuePatterns := (&ExternalGrandmasterProperties{}).ValueRegEx()
	oldValuePatterns[keyGMIdentity] = `\d*\.\d*\.\d*`

	oldBuggyPattern := regexp.MustCompile(
		buildDataSetRegex(
			(&ExternalGrandmasterProperties{}).Keys(),
			oldValuePatterns,
			true,
			[]string{},
		),
	)
	newFixedPattern := regexp.MustCompile((&ExternalGrandmasterProperties{}).RegEx())

	realResponses := []string{
		" gmIdentity 507c6f.fffe.1fb16c  stepsRemoved        2 \n",
		" gmIdentity 8faf00.fffe.cf0f3b  stepsRemoved        0 \n",
	}

	for _, resp := range realResponses {
		assert.Nilf(t, oldBuggyPattern.FindStringSubmatch(resp),
			"sanity check failed: old decimal-only pattern unexpectedly matched %q", resp)
		assert.NotNilf(t, newFixedPattern.FindStringSubmatch(resp),
			"fixed pattern should match real hex identity %q", resp)
	}
}

func TestExternalGrandmasterProperties_ValueRegEx_GMIdentityShape(t *testing.T) {
	valueRe := regexp.MustCompile(`^` + (&ExternalGrandmasterProperties{}).ValueRegEx()[keyGMIdentity] + `$`)

	tests := []struct {
		name      string
		gmID      string
		wantMatch bool
	}{
		{"real upstream identity", realGMIdentityUpstream, true},
		{"real local identity", realGMIdentityLocal, true},
		{"uppercase hex", "AABBCC.FFFE.DDEEFF", true},
		{"mixed case hex", "AaBbCc.fFfE.DdEeFf", true},
		{"all-zero identity", "000000.0000.000000", true},
		{"decimal-only identity", "001122.334455.667788", true},
		{"non-hex letter g", "gggggg.fffe.gggggg", false},
		{"missing a segment", "507c6f.fffe", false},
		{"empty segment", "507c6f..1fb16c", false},
		{"empty string", "", false},
		{"trailing garbage", "507c6f.fffe.1fb16c.extra", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := valueRe.MatchString(tt.gmID)
			assert.Equal(t, tt.wantMatch, got, "gmIdentity=%q", tt.gmID)
		})
	}
}

func TestExternalGrandmasterProperties_Update(t *testing.T) {
	e := &ExternalGrandmasterProperties{}
	e.Update(keyGMIdentity, realGMIdentityUpstream)
	e.Update(keyStepsRemoved, "2")

	assert.Equal(t, realGMIdentityUpstream, e.GrandmasterIdentity)
	assert.Equal(t, uint16(2), e.StepsRemoved)
}

func TestExternalGrandmasterProperties_Update_UnknownKeyIsNoop(t *testing.T) {
	e := &ExternalGrandmasterProperties{GrandmasterIdentity: realGMIdentityUpstream, StepsRemoved: 1}
	e.Update("someUnknownField", "whatever")

	assert.Equal(t, realGMIdentityUpstream, e.GrandmasterIdentity)
	assert.Equal(t, uint16(1), e.StepsRemoved)
}

func TestExternalGrandmasterProperties_Keys(t *testing.T) {
	assert.Equal(t, []string{keyGMIdentity, keyStepsRemoved}, (&ExternalGrandmasterProperties{}).Keys())
}

func TestExternalGrandmasterProperties_String(t *testing.T) {
	e := &ExternalGrandmasterProperties{GrandmasterIdentity: realGMIdentityUpstream, StepsRemoved: 2}
	got := e.String()

	assert.Contains(t, got, "gmIdentity "+realGMIdentityUpstream)
	assert.Contains(t, got, "stepsRemoved")
	assert.Contains(t, got, "2")
}

func TestExternalGrandmasterProperties_String_NilReceiver(t *testing.T) {
	var e *ExternalGrandmasterProperties
	assert.Equal(t, "", e.String())
}

// TestExternalGrandmasterProperties_RegExRoundTrip drives the exact pipeline
// pkg/pmc uses in production: compile RegEx(), match a captured pmc response
// with FindStringSubmatch, then feed matches[1:] through Update() in Keys()
// order (see RunPMCExpGetExternalGMPropertiesNP /
// RunPMCExpSetExternalGMPropertiesNP), and assert the resulting struct is
// correct end to end.
func TestExternalGrandmasterProperties_RegExRoundTrip(t *testing.T) {
	re := regexp.MustCompile((&ExternalGrandmasterProperties{}).RegEx())
	response := "\tb4e9b8.fffe.5ec71a-0 seq 0 RESPONSE MANAGEMENT EXTERNAL_GRANDMASTER_PROPERTIES_NP \n" +
		"\t\tgmIdentity   " + realGMIdentityUpstream + "\n" +
		"\t\tstepsRemoved 2\n"

	matches := re.FindStringSubmatch(response)
	require.NotNil(t, matches, "regex should match a real pmc RESPONSE MANAGEMENT body")

	var egp ExternalGrandmasterProperties
	for i, m := range matches[1:] {
		egp.Update(egp.Keys()[i], m)
	}

	assert.Equal(t, realGMIdentityUpstream, egp.GrandmasterIdentity)
	assert.Equal(t, uint16(2), egp.StepsRemoved)
}

// TestExternalGrandmasterProperties_ProcessMessage exercises the generic
// ProcessMessage helper (used by the multi-command batch PMC readers) with
// ExternalGrandmasterProperties as the concrete DataSet.
func TestExternalGrandmasterProperties_ProcessMessage(t *testing.T) {
	re := regexp.MustCompile((&ExternalGrandmasterProperties{}).RegEx())
	response := " gmIdentity " + realGMIdentityLocal + "  stepsRemoved        0 \n"

	matches := re.FindStringSubmatch(response)
	require.NotNil(t, matches)

	egp, err := ProcessMessage[ExternalGrandmasterProperties](matches)
	require.NoError(t, err)
	assert.Equal(t, realGMIdentityLocal, egp.GrandmasterIdentity)
	assert.Equal(t, uint16(0), egp.StepsRemoved)
}

func TestExternalGrandmasterProperties_MonitorRegEx_CompilesWithoutCaptureGroups(t *testing.T) {
	pattern := (&ExternalGrandmasterProperties{}).MonitorRegEx()
	re, err := regexp.Compile(pattern)
	require.NoError(t, err)
	assert.Equal(t, 0, re.NumSubexp(), "MonitorRegEx must not declare capture groups")

	response := " gmIdentity " + realGMIdentityUpstream + "  stepsRemoved        2 \n"
	assert.True(t, re.MatchString(response))
}

// ParentDataSet.grandmasterIdentity/parentPortIdentity used to be matched
// with a catch-all `.*`, which "worked" but was imprecise and inconsistent
// with the (now-fixed) ExternalGrandmasterProperties.gmIdentity pattern.
// Both are tightened to the same clockIdentityPattern (parentPortIdentity
// additionally requires the real "-<portNumber>" suffix ptp4l always
// appends, e.g. c45ab1.ffff.545c05-21).
func TestParentDataSet_RegEx_MatchesRealPmcResponse(t *testing.T) {
	re := regexp.MustCompile((&ParentDataSet{}).RegEx())

	// Captured live via `pmc -u -b 0 -f /var/run/ptp4l.0.config 'GET PARENT_DATA_SET'`.
	response := "\tb4e9b8.fffe.5ec71a-0 seq 0 RESPONSE MANAGEMENT PARENT_DATA_SET \n" +
		"\t\tparentPortIdentity                    b4e9b8.fffe.5ec71a-0\n" +
		"\t\tparentStats                           0\n" +
		"\t\tobservedParentOffsetScaledLogVariance 0xffff\n" +
		"\t\tobservedParentClockPhaseChangeRate    0x7fffffff\n" +
		"\t\tgrandmasterPriority1                  128\n" +
		"\t\tgm.ClockClass                         6\n" +
		"\t\tgm.ClockAccuracy                      0x21\n" +
		"\t\tgm.OffsetScaledLogVariance            0x4e5d\n" +
		"\t\tgrandmasterPriority2                  128\n" +
		"\t\tgrandmasterIdentity                   " + realGMIdentityUpstream + "\n"

	matches := re.FindStringSubmatch(response)
	require.NotNil(t, matches, "regex should match a real pmc RESPONSE MANAGEMENT PARENT_DATA_SET body")

	var pds ParentDataSet
	for i, m := range matches[1:] {
		pds.Update(pds.Keys()[i], m)
	}

	assert.Equal(t, "b4e9b8.fffe.5ec71a-0", pds.ParentPortIdentity)
	assert.Equal(t, realGMIdentityUpstream, pds.GrandmasterIdentity)
	assert.Equal(t, uint8(6), pds.GrandmasterClockClass)
}

func TestParentDataSet_ValueRegEx_GrandmasterIdentityShape(t *testing.T) {
	valueRe := regexp.MustCompile(`^` + (&ParentDataSet{}).ValueRegEx()["grandmasterIdentity"] + `$`)

	tests := []struct {
		name      string
		id        string
		wantMatch bool
	}{
		{"real identity", realGMIdentityUpstream, true},
		{"uppercase hex", "AABBCC.FFFE.DDEEFF", true},
		{"non-hex letter g", "gggggg.fffe.gggggg", false},
		{"missing a segment", "507c6f.fffe", false},
		{"empty string", "", false},
		{"port-suffixed value (belongs to parentPortIdentity, not here)", realGMIdentityUpstream + "-0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantMatch, valueRe.MatchString(tt.id), "grandmasterIdentity=%q", tt.id)
		})
	}
}

func TestParentDataSet_ValueRegEx_ParentPortIdentityShape(t *testing.T) {
	valueRe := regexp.MustCompile(`^` + (&ParentDataSet{}).ValueRegEx()["parentPortIdentity"] + `$`)

	tests := []struct {
		name      string
		id        string
		wantMatch bool
	}{
		{"real capture, port 0", "b4e9b8.fffe.5ec71a-0", true},
		{"real capture, port 21", "c45ab1.ffff.545c05-21", true},
		{"real capture, port 9", "c45ab1.ffff.545c05-9", true},
		{"missing port suffix", realGMIdentityUpstream, false},
		{"non-numeric port suffix", realGMIdentityUpstream + "-a", false},
		{"empty string", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantMatch, valueRe.MatchString(tt.id), "parentPortIdentity=%q", tt.id)
		})
	}
}

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
