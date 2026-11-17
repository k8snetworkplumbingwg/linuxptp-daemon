package topology

import (
	"strings"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

func TestNewProfileTopologyRejectsUnsupportedNestedGraph(t *testing.T) {
	profile := func(name string, settings map[string]string) *ptpv1.PtpProfile {
		return &ptpv1.PtpProfile{Name: &name, PtpSettings: settings}
	}
	profiles := []*ptpv1.PtpProfile{
		profile("ha", map[string]string{"haProfiles": "bc, backup"}),
		profile("bc", map[string]string{"clockType": string(event.TGM)}),
		profile("tr", map[string]string{"clockType": string(event.TGM), "controllingProfile": "bc"}),
		profile("backup", map[string]string{"clockType": string(event.TGM)}),
	}
	phc2sys := "-a"
	profiles[0].Phc2sysOpts = &phc2sys
	topology, err := NewProfileTopology(profiles)
	if err == nil || !strings.Contains(err.Error(), `T-GM clock "bc" has unsupported children`) {
		t.Fatalf("expected unsupported children error, got %v", err)
	}
	if len(topology.graph) != 1 || topology.graph[0].profileName != "ha" {
		t.Fatalf("unexpected roots: %+v", topology.graph)
	}
	children := topology.graph[0].children
	if len(children) != 2 || children[0].profileName != "bc" || children[1].profileName != "backup" {
		t.Fatalf("unexpected HA children: %+v", children)
	}
	if len(children[0].children) != 1 || children[0].children[0].profileName != "tr" {
		t.Fatalf("unexpected nested child: %+v", children[0].children)
	}
}

func TestNewProfileTopologyPopulatesGraph(t *testing.T) {
	name := "gm"
	topology, err := NewProfileTopology([]*ptpv1.PtpProfile{{
		Name: &name, PtpSettings: map[string]string{"clockType": string(event.TGM)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(topology.graph) != 1 || topology.graph[0].profileName != name {
		t.Fatalf("unexpected graph: %+v", topology.graph)
	}
}

func TestNewProfileTopologySkipsNilProfile(t *testing.T) {
	name := "gm"
	topology, err := NewProfileTopology([]*ptpv1.PtpProfile{nil, {
		Name: &name, PtpSettings: map[string]string{"clockType": string(event.TGM)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(topology.entries) != 1 || topology.entries[name].RunID != 1 {
		t.Fatalf("entries = %+v, want only %s with run ID 1", topology.entries, name)
	}
}

func TestNewProfileTopologyAcceptsBCTransmitter(t *testing.T) {
	controller, child := "controller", "child"
	bcConf := "[global]\n[ens1f0]\nmasterOnly 0\n[ens1f1]\nmasterOnly 1\n"
	profiles := []*ptpv1.PtpProfile{
		{Name: &controller, PtpSettings: map[string]string{"clockType": string(event.TBC)}},
		{Name: &child, Ptp4lConf: &bcConf, PtpSettings: map[string]string{"controllingProfile": controller}},
	}
	for _, ordered := range [][]*ptpv1.PtpProfile{profiles, {profiles[1], profiles[0]}} {
		topology, err := NewProfileTopology(ordered)
		if err != nil {
			t.Fatal(err)
		}
		if topology.entries[controller].ClockType != event.TBC || topology.entries[child].ClockType != event.BC {
			t.Fatalf("unexpected clock types: controller=%s child=%s", topology.entries[controller].ClockType, topology.entries[child].ClockType)
		}
	}
}

func TestNewProfileTopologyValidation(t *testing.T) {
	name := func(value string) *string { return &value }
	for _, tc := range []struct {
		name     string
		profiles []*ptpv1.PtpProfile
		want     string
	}{
		{"HA with empty ptp4l options", []*ptpv1.PtpProfile{
			{Name: name("ha"), PtpSettings: map[string]string{"haProfiles": "member"}, Phc2sysOpts: name("-a"), Ptp4lOpts: name("")},
			{Name: name("member"), Ptp4lConf: name("[global]\n[ens1f0]\nclientOnly 1\n")},
		}, ""},
		{"HA with ptp4l config", []*ptpv1.PtpProfile{
			{Name: name("ha"), PtpSettings: map[string]string{"haProfiles": "member"}, Phc2sysOpts: name("-a"), Ptp4lConf: name("[global]")},
			{Name: name("member"), PtpSettings: map[string]string{"clockType": string(event.TGM)}},
		}, "cannot run ptp4l"},
		{"HA with ptp4l options", []*ptpv1.PtpProfile{
			{Name: name("ha"), PtpSettings: map[string]string{"haProfiles": "member"}, Phc2sysOpts: name("-a"), Ptp4lOpts: name("-m")},
			{Name: name("member"), PtpSettings: map[string]string{"clockType": string(event.TGM)}},
		}, "cannot run ptp4l"},
		{"multiple phc2sys", []*ptpv1.PtpProfile{
			{Name: name("first"), PtpSettings: map[string]string{"clockType": string(event.TGM)}, Phc2sysOpts: name("-a")},
			{Name: name("second"), PtpSettings: map[string]string{"clockType": string(event.TGM)}, Phc2sysOpts: name("-a")},
		}, "multiple processes are configured to run phc2sys"},
		{"T-BC controls two profiles", []*ptpv1.PtpProfile{
			{Name: name("controller"), PtpSettings: map[string]string{"clockType": string(event.TBC)}},
			{Name: name("first"), PtpSettings: map[string]string{"clockType": string(event.TBC), "controllingProfile": "controller"}},
			{Name: name("second"), PtpSettings: map[string]string{"clockType": string(event.TBC), "controllingProfile": "controller"}},
		}, "controls multiple profiles"},
		{"T-BC controller chain", []*ptpv1.PtpProfile{
			{Name: name("first"), PtpSettings: map[string]string{"clockType": string(event.TBC)}},
			{Name: name("second"), PtpSettings: map[string]string{"clockType": string(event.TBC), "controllingProfile": "first"}},
			{Name: name("third"), PtpSettings: map[string]string{"clockType": string(event.TBC), "controllingProfile": "second"}},
		}, "controller chain is unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewProfileTopology(tc.profiles)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
}

func TestVerifyTraversesGraph(t *testing.T) {
	topology := ProfileTopology{
		entries: map[string]*ClockEntry{"parent": {ClockType: event.HA, RunsPhc2Sys: true}},
		graph:   []*GraphNode{{profileName: "parent", children: []*GraphNode{{profileName: "missing"}}}},
	}
	err := topology.verify()
	if err == nil || !strings.Contains(err.Error(), `missing entry for profile "missing"`) {
		t.Fatalf("expected missing child entry error, got %v", err)
	}
	if !strings.Contains(err.Error(), "HA base node parent") {
		t.Fatalf("expected HA-specific missing member error, got %v", err)
	}
	topology.entries["parent"].ClockType = event.TBC
	if err = topology.verify(); err == nil || !strings.Contains(err.Error(), "T-BC base node parent") {
		t.Fatalf("expected T-BC-specific missing child error, got %v", err)
	}
}

func TestNewProfileTopologyRejectsInvalidRelationships(t *testing.T) {
	profile := func(name, controller string) *ptpv1.PtpProfile {
		return &ptpv1.PtpProfile{Name: &name, PtpSettings: map[string]string{"clockType": string(event.TGM), "controllingProfile": controller}}
	}
	for _, tc := range []struct {
		name     string
		profiles []*ptpv1.PtpProfile
		want     string
	}{
		{"missing", []*ptpv1.PtpProfile{profile("a", "missing")}, "missing entry"},
		{"cycle", []*ptpv1.PtpProfile{profile("a", "b"), profile("b", "a")}, "cycle"},
		{"cycle beside root", []*ptpv1.PtpProfile{profile("root", ""), profile("a", "b"), profile("b", "a")}, "cycle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			topology, err := NewProfileTopology(tc.profiles)
			if tc.name == "missing" && (len(topology.graph) != 1 || topology.graph[0].profileName != "a") {
				t.Fatalf("entry with no graph parent should be a root: %+v", topology.graph)
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
	ha := profile("ha", "")
	ha.PtpSettings["haProfiles"] = "bc"
	bc := profile("bc", "other")
	other := profile("other", "")
	_, err := NewProfileTopology([]*ptpv1.PtpProfile{ha, bc, other})
	if err == nil || !strings.Contains(err.Error(), "multiple parents") {
		t.Fatalf("expected multiple parents error, got %v", err)
	}
}
