package topology

import (
	"fmt"
	"strings"

	"github.com/go-openapi/swag/conv"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ptpconfig"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

// ProfileTopology Represents the graph of clocks made by profiles
type ProfileTopology struct {
	entries map[string]*ClockEntry
	graph   []*GraphNode
}

// ClockEntry ...
type ClockEntry struct {
	Profile       *ptpv1.PtpProfile
	Ptp4lConfig   ptpconfig.Ptp4lConf
	ClockType     event.ClockType
	RunID         uint
	LeadingNic    string
	UpstreamPorts []string
	RunsPhc2Sys   bool
	NtpFailOver   bool
}

// GraphNode ...
type GraphNode struct {
	profileName string
	children    []*GraphNode
}

// NewClockEntry  ...
func NewClockEntry(runID uint, profile *ptpv1.PtpProfile) (*ClockEntry, error) {
	node := &ClockEntry{
		RunID:       runID,
		Profile:     profile,
		RunsPhc2Sys: ptpconfig.ProcessShouldRun(profile, event.Phc2SysProcessName),
		LeadingNic:  profile.PtpSettings["leadingInterface"],
	}
	node.NtpFailOver = node.RunsPhc2Sys && profile.Plugins != nil && profile.Plugins["ntpfailover"] != nil

	clockType := event.ClockUnset
	profileClockType, profileClockTypefound := profile.PtpSettings["clockType"]
	if profileClockTypefound {
		switch profileClockType {
		case string(event.TGM):
			clockType = event.TGM
		case string(event.TBC):
			clockType = event.TBC
			if portsStr, ok := profile.PtpSettings["upstreamPort"]; ok {
				node.UpstreamPorts = strings.Split(portsStr, ",")
			}
		}
	}
	if len(utils.ListHAMemberProfiles(profile)) > 0 {
		clockType = event.HA
	} else if clockType == event.ClockUnset && profile.Ptp4lConf != nil {
		// If unset default to clock type inferred from ptp4l
		// Parsing ptp4l needs to be done here to get the fallback clock type.
		// Needs to be done outside the loop as we need to guarantee clockType
		// set before the event.Ts2PhcProcessName case where it is used.
		err := node.Ptp4lConfig.PopulatePtp4lConf(profile.Ptp4lConf, profile.Ptp4lOpts)
		if err != nil {
			return node, err
		}
		clockType = node.Ptp4lConfig.ClockType
	}
	node.ClockType = clockType
	return node, nil
}

// NewProfileTopology ...
func NewProfileTopology(nodeProfiles []*ptpv1.PtpProfile) (ProfileTopology, error) {
	result := ProfileTopology{
		entries: make(map[string]*ClockEntry),
	}
	nodes := make(map[string]*GraphNode, len(nodeProfiles))
	childNode := make(map[string]bool)

	// Initial population
	nNil := 0
	for runID, profile := range nodeProfiles {
		if profile == nil {
			continue
		}
		profileName := utils.GetProfileName(profile)
		if profileName == "<nil>" {
			profileName += conv.FormatInteger(nNil)
			nNil++
		}
		profile.Name = &profileName
		if _, exists := result.entries[profileName]; exists {
			return result, fmt.Errorf("duplicate profile name %q", profileName)
		}

		entry, err := NewClockEntry(uint(runID), profile)
		if err != nil {
			return result, err
		}
		result.entries[profileName] = entry

		if nodes[profileName] == nil {
			nodes[profileName] = &GraphNode{profileName: profileName}
		}

		if entry.ClockType == event.HA {
			for _, memberName := range utils.ListHAMemberProfiles(profile) {
				if nodes[memberName] == nil {
					nodes[memberName] = &GraphNode{profileName: memberName}
				}
				nodes[profileName].children = append(nodes[profileName].children, nodes[memberName])
				childNode[memberName] = true
			}
		}

		if controller := profile.PtpSettings["controllingProfile"]; controller != "" {
			if nodes[controller] == nil {
				nodes[controller] = &GraphNode{profileName: controller}
			}
			nodes[controller].children = append(nodes[controller].children, nodes[profileName])
		}
	}

	for name, entry := range result.entries {
		controller := entry.Profile.PtpSettings["controllingProfile"]
		if !childNode[name] && (controller == "" || result.entries[controller] == nil) {
			result.graph = append(result.graph, nodes[name])
		}
	}
	return result, result.verify()
}

// BuildClocks creates one clock for each graph root.
func (t ProfileTopology) BuildClocks() ([]clock.Clock, error) {
	if err := t.verify(); err != nil {
		return nil, err
	}
	clocks := make([]clock.Clock, 0, len(t.graph))
	for _, root := range t.graph {
		instance, err := t.buildClock(root)
		if err != nil {
			return nil, err
		}
		clocks = append(clocks, instance)
	}
	return clocks, nil
}

func (t ProfileTopology) buildClock(node *GraphNode) (clock.Clock, error) {
	entry := t.entries[node.profileName]
	ptp4lConfig := fmt.Sprintf("ptp4l.%d.config", entry.RunID)
	switch entry.ClockType {
	case event.HA:
		members := make([]clock.HAMember, 0, len(node.children))
		for _, child := range node.children {
			memberClock, err := t.buildClock(child)
			if err != nil {
				return nil, err
			}
			members = append(members, clock.HAMember{Profile: child.profileName, Clock: memberClock.(*clock.BCClock)})
		}
		return clock.NewHA(fmt.Sprintf("phc2sys.%d.config", entry.RunID), members...)
	case event.TBC:
		child := t.entries[node.children[0].profileName]
		controlledConfig := fmt.Sprintf("ptp4l.%d.config", child.RunID)
		tbc, err := clock.NewTBC(ptp4lConfig, leap.GetUtcOffset, pmc.ActiveClient(), controlledConfig)
		if err != nil {
			return nil, err
		}
		if entry.LeadingNic != "" {
			tbc.SetConfiguredLeadingInterface(entry.LeadingNic)
		}
		return tbc, nil
	case event.TTSC:
		ttsc, err := clock.NewTBC(ptp4lConfig, leap.GetUtcOffset, pmc.ActiveClient(), "")
		if err != nil {
			return nil, err
		}
		if entry.LeadingNic != "" {
			ttsc.SetConfiguredLeadingInterface(entry.LeadingNic)
		}
		return ttsc, nil
	case event.BC, event.OC:
		return clock.NewBC(ptp4lConfig, entry.ClockType == event.OC, ptpconfig.ResolvePTPThreshold(entry.Profile))
	case event.TGM:
		return clock.NewGM(ptp4lConfig, leap.GetUtcOffset, pmc.ActiveClient())
	}
	return nil, fmt.Errorf("unsupported clock type %q", entry.ClockType)
}
