package topology

import (
	"strings"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

func TestVerifyClockRules(t *testing.T) {
	for _, tc := range []struct {
		name     string
		topology ProfileTopology
		want     string
	}{
		{
			name: "HA requires phc2sys",
			topology: ProfileTopology{
				entries: map[string]*ClockEntry{"ha": {ClockType: event.HA}, "member": {ClockType: event.BC}},
				graph:   []*GraphNode{{profileName: "ha", children: []*GraphNode{{profileName: "member"}}}},
			},
			want: "HA profile without phc2sys configured",
		},
		{
			name: "HA requires members",
			topology: ProfileTopology{
				entries: map[string]*ClockEntry{"ha": {ClockType: event.HA, RunsPhc2Sys: true}},
				graph:   []*GraphNode{{profileName: "ha"}},
			},
			want: "HA profile with no members",
		},
		{
			name: "HA members must be BC or OC",
			topology: ProfileTopology{
				entries: map[string]*ClockEntry{"ha": {ClockType: event.HA, RunsPhc2Sys: true}, "member": {ClockType: event.TGM}},
				graph:   []*GraphNode{{profileName: "ha", children: []*GraphNode{{profileName: "member"}}}},
			},
			want: `HA member "member" is not a BC or OC clock`,
		},
		{
			name: "unknown clock type",
			topology: ProfileTopology{
				entries: map[string]*ClockEntry{"bad": {ClockType: event.ClockType("unknown")}},
				graph:   []*GraphNode{{profileName: "bad"}},
			},
			want: "unhandled clock type",
		},
		{
			name: "missing clock entry",
			topology: ProfileTopology{
				graph: []*GraphNode{{profileName: "missing"}},
			},
			want: `missing entry for profile "missing"`,
		},
		{
			name: "missing controller entry",
			topology: ProfileTopology{
				entries: map[string]*ClockEntry{"child": {
					ClockType: event.BC, Profile: &ptpv1.PtpProfile{PtpSettings: map[string]string{"controllingProfile": "missing"}},
				}},
				graph: []*GraphNode{{profileName: "child"}},
			},
			want: `missing entry for controlling profile "missing"`,
		},
		{
			name: "entry absent from graph",
			topology: ProfileTopology{
				entries: map[string]*ClockEntry{"orphan": {ClockType: event.BC}},
			},
			want: "cycle or unreachable profile in graph",
		},
		{
			name: "T-GM cannot have children",
			topology: ProfileTopology{
				entries: map[string]*ClockEntry{"parent": {ClockType: event.TGM}, "child": {ClockType: event.TTSC}},
				graph:   []*GraphNode{{profileName: "parent", children: []*GraphNode{{profileName: "child"}}}},
			},
			want: `T-GM clock "parent" has unsupported children`,
		},
		{
			name: "BC cannot have children",
			topology: ProfileTopology{
				entries: map[string]*ClockEntry{"parent": {ClockType: event.BC}, "child": {ClockType: event.TTSC}},
				graph:   []*GraphNode{{profileName: "parent", children: []*GraphNode{{profileName: "child"}}}},
			},
			want: `BC clock "parent" has unsupported children`,
		},
		{
			name: "OC cannot have children",
			topology: ProfileTopology{
				entries: map[string]*ClockEntry{"parent": {ClockType: event.OC}, "child": {ClockType: event.TTSC}},
				graph:   []*GraphNode{{profileName: "parent", children: []*GraphNode{{profileName: "child"}}}},
			},
			want: `OC clock "parent" has unsupported children`,
		},
		{
			name: "TTSC cannot have children",
			topology: ProfileTopology{
				entries: map[string]*ClockEntry{"parent": {ClockType: event.TTSC}, "child": {ClockType: event.BC}},
				graph:   []*GraphNode{{profileName: "parent", children: []*GraphNode{{profileName: "child"}}}},
			},
			want: `T-TSC clock "parent" has unsupported children`,
		},
		{
			name: "T-BC requires a BC transmitter",
			topology: ProfileTopology{
				entries: map[string]*ClockEntry{"parent": {ClockType: event.TBC}, "child": {ClockType: event.OC}},
				graph:   []*GraphNode{{profileName: "parent", children: []*GraphNode{{profileName: "child"}}}},
			},
			want: `T-BC controlled profile "child" is not a BC clock`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.topology.verify(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}

	for _, clockType := range []event.ClockType{event.TGM, event.BC, event.OC, event.TTSC} {
		t.Run(string(clockType), func(t *testing.T) {
			topology := ProfileTopology{
				entries: map[string]*ClockEntry{"clock": {ClockType: clockType}},
				graph:   []*GraphNode{{profileName: "clock"}},
			}
			if err := topology.verify(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestVerifyGraphCycle(t *testing.T) {
	a := &GraphNode{profileName: "a"}
	b := &GraphNode{profileName: "b"}
	a.children = []*GraphNode{b}
	b.children = []*GraphNode{a}
	topology := ProfileTopology{
		entries: map[string]*ClockEntry{"a": {ClockType: event.BC}, "b": {ClockType: event.BC}},
		graph:   []*GraphNode{a},
	}
	if err := topology.verify(); err == nil || !strings.Contains(err.Error(), "cycle in profile graph") {
		t.Fatalf("expected cycle error, got %v", err)
	}
}

func TestVerifyTBCWithoutChild(t *testing.T) {
	if err := verifyTBC(&GraphNode{profileName: "controller"}, ProfileTopology{}); err == nil || !strings.Contains(err.Error(), "missing TT profile") {
		t.Fatalf("expected missing TT profile error, got %v", err)
	}
}

func TestVerifyTurnsLeafTBCIntoTTSC(t *testing.T) {
	entry := &ClockEntry{ClockType: event.TBC}
	topology := ProfileTopology{
		entries: map[string]*ClockEntry{"leaf": entry},
		graph:   []*GraphNode{{profileName: "leaf"}},
	}
	if err := topology.verify(); err != nil {
		t.Fatal(err)
	}
	if entry.ClockType != event.TTSC {
		t.Fatalf("leaf clock type = %s, want %s", entry.ClockType, event.TTSC)
	}
}
