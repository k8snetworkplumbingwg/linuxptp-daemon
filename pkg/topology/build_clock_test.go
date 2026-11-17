package topology

import (
	"slices"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

func TestBuildClocksFromGraphPOC(t *testing.T) {
	haName, bc1Name, ocName := "ha", "bc1", "oc"
	topology := ProfileTopology{entries: map[string]*ClockEntry{
		"ha":  {Profile: &ptpv1.PtpProfile{Name: &haName, PtpSettings: map[string]string{"haProfiles": "bc1,oc"}}, ClockType: event.HA, RunID: 2, RunsPhc2Sys: true},
		"bc1": {Profile: &ptpv1.PtpProfile{Name: &bc1Name}, ClockType: event.BC, RunID: 0},
		"oc":  {Profile: &ptpv1.PtpProfile{Name: &ocName}, ClockType: event.OC, RunID: 1},
	}, graph: []*GraphNode{{profileName: "ha", children: []*GraphNode{{profileName: "bc1"}, {profileName: "oc"}}}}}
	clocks, err := topology.BuildClocks()
	if err != nil {
		t.Fatal(err)
	}
	if len(clocks) != 1 {
		t.Fatalf("clocks = %d, want 1", len(clocks))
	}
	instance := clocks[0]
	if _, ok := instance.(*clock.HAClock); !ok {
		t.Fatalf("clock = %T, want *clock.HAClock", instance)
	}
	for _, config := range []string{"phc2sys.2.config", "ptp4l.0.config", "ptp4l.1.config"} {
		if !slices.Contains(instance.ConfigNames(), config) {
			t.Fatalf("HA clock does not route %s: %v", config, instance.ConfigNames())
		}
	}

	controllerName, childName := "controller", "child"
	tbc := ProfileTopology{entries: map[string]*ClockEntry{
		"controller": {Profile: &ptpv1.PtpProfile{Name: &controllerName}, ClockType: event.TBC, RunID: 3},
		"child":      {Profile: &ptpv1.PtpProfile{Name: &childName, PtpSettings: map[string]string{"controllingProfile": "controller"}}, ClockType: event.BC, RunID: 4},
	}, graph: []*GraphNode{{profileName: "controller", children: []*GraphNode{{profileName: "child"}}}}}
	clocks, err = tbc.BuildClocks()
	if err != nil {
		t.Fatal(err)
	}
	if len(clocks) != 1 {
		t.Fatalf("clocks = %d, want 1", len(clocks))
	}
	instance = clocks[0]
	if _, ok := instance.(*clock.TBC); !ok || instance.ConfigName() != "ptp4l.3.config" || tbc.entries["child"].ClockType != event.BC {
		t.Fatalf("T-BC graph built %T (%s), child type %s", instance, instance.ConfigName(), tbc.entries["child"].ClockType)
	}
}

func TestBuildClocksPOCMultipleRoots(t *testing.T) {
	bcName, ocName := "bc", "oc"
	topology := ProfileTopology{entries: map[string]*ClockEntry{
		"bc": {Profile: &ptpv1.PtpProfile{Name: &bcName}, ClockType: event.BC, RunID: 0},
		"oc": {Profile: &ptpv1.PtpProfile{Name: &ocName}, ClockType: event.OC, RunID: 1},
	}, graph: []*GraphNode{{profileName: "bc"}, {profileName: "oc"}}}
	clocks, err := topology.BuildClocks()
	if err != nil {
		t.Fatal(err)
	}
	if len(clocks) != 2 {
		t.Fatalf("clocks = %d, want 2", len(clocks))
	}
	types := map[event.ClockType]bool{}
	for _, instance := range clocks {
		types[instance.ClockType()] = true
	}
	if !types[event.BC] || !types[event.OC] {
		t.Fatalf("clock types = %v, want BC and OC", types)
	}
}

func TestBuildClocksLeafTBCBecomesTTSC(t *testing.T) {
	name := "leaf"
	topology := ProfileTopology{
		entries: map[string]*ClockEntry{name: {Profile: &ptpv1.PtpProfile{Name: &name}, ClockType: event.TBC, RunID: 7}},
		graph:   []*GraphNode{{profileName: name}},
	}
	clocks, err := topology.BuildClocks()
	if err != nil {
		t.Fatal(err)
	}
	if len(clocks) != 1 {
		t.Fatalf("clocks = %d, want 1", len(clocks))
	}
	if _, ok := clocks[0].(*clock.TBC); !ok || topology.entries[name].ClockType != event.TTSC || clocks[0].ConfigName() != "ptp4l.7.config" {
		t.Fatalf("clock = %T at %s, entry type %s; want TTSC entry at ptp4l.7.config", clocks[0], clocks[0].ConfigName(), topology.entries[name].ClockType)
	}
}
