package topology

import (
	"errors"
	"fmt"
	"strings"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
)

const (
	nodeVisiting uint8 = 1
	nodeVisited  uint8 = 2
)

func verifyHA(node *GraphNode, entry *ClockEntry, topology ProfileTopology) error {
	var result []error
	if !entry.RunsPhc2Sys {
		result = append(result, fmt.Errorf("HA profile without phc2sys configured: %s", node.profileName))
	}
	if entry.Profile != nil && ((entry.Profile.Ptp4lOpts != nil && strings.TrimSpace(*entry.Profile.Ptp4lOpts) != "") ||
		(entry.Profile.Ptp4lConf != nil && strings.TrimSpace(*entry.Profile.Ptp4lConf) != "")) {
		result = append(result, fmt.Errorf("HA profile %s cannot run ptp4l", node.profileName))
	}
	if len(node.children) == 0 {
		result = append(result, fmt.Errorf("HA profile with no members: %s", node.profileName))
	}
	var missing []string
	for _, child := range node.children {
		member := topology.entries[child.profileName]
		if member == nil {
			missing = append(missing, child.profileName)
		} else if member.ClockType != event.BC && member.ClockType != event.OC {
			result = append(result, fmt.Errorf("HA member %q is not a BC or OC clock", child.profileName))
		}
	}
	if len(missing) > 0 {
		result = append(result, fmt.Errorf("the following profiles are missing from graph with HA base node %s: [%s]", node.profileName, strings.Join(missing, ", ")))
	}
	return errors.Join(result...)
}

func verifyTBC(node *GraphNode, topology ProfileTopology) error {
	if len(node.children) == 0 {
		return fmt.Errorf("%s is missing TT profile", node.profileName)
	}
	var result []error
	if len(node.children) > 1 {
		result = append(result, fmt.Errorf("T-BC profile %s controls multiple profiles", node.profileName))
	}
	var missing []string
	for _, child := range node.children {
		if entry := topology.entries[child.profileName]; entry == nil {
			missing = append(missing, child.profileName)
		} else if entry.ClockType != event.BC {
			result = append(result, fmt.Errorf("T-BC controlled profile %q is not a BC clock", child.profileName))
		}
		if len(child.children) > 0 {
			result = append(result, fmt.Errorf("controller chain is unsupported: %s controls %s, which controls another profile", node.profileName, child.profileName))
		}
	}
	if len(missing) > 0 {
		result = append(result, fmt.Errorf("the following profiles are missing from graph with T-BC base node %s: [%s]", node.profileName, strings.Join(missing, ", ")))
	}
	return errors.Join(result...)
}

func verifyNode(node *GraphNode, entry *ClockEntry, topology ProfileTopology) error {
	if entry.ClockType == event.TBC && len(node.children) == 0 {
		entry.ClockType = event.TTSC
	}
	switch entry.ClockType {
	case event.HA:
		return verifyHA(node, entry, topology)
	case event.TBC:
		return verifyTBC(node, topology)
	case event.BC, event.OC, event.TGM, event.TTSC:
		if len(node.children) > 0 {
			names := []string{}
			for _, ch := range node.children {
				names = append(names, ch.profileName)
			}
			return fmt.Errorf("%s clock %q has unsupported children: [%s]", entry.ClockType, node.profileName, strings.Join(names, ", "))
		}
		return nil
	}
	return fmt.Errorf("%s has an unhandled clock type: %s", node.profileName, entry.ClockType)
}

func verifyGraphNode(node *GraphNode, topology ProfileTopology, visitState map[*GraphNode]uint8, visitedEntries map[string]bool) ([]string, []error) {
	if visitState[node] == nodeVisiting {
		return nil, []error{fmt.Errorf("cycle in profile graph at %q", node.profileName)}
	}
	if visitState[node] == nodeVisited {
		return nil, []error{fmt.Errorf("profile %q has multiple parents", node.profileName)}
	}
	visitState[node] = nodeVisiting
	var runsPhc2Sys []string
	var result []error
	entry := topology.entries[node.profileName]
	if entry == nil {
		result = append(result, fmt.Errorf("missing entry for profile %q", node.profileName))
	} else {
		visitedEntries[node.profileName] = true
		if entry.Profile != nil {
			if controller := entry.Profile.PtpSettings["controllingProfile"]; controller != "" && topology.entries[controller] == nil {
				result = append(result, fmt.Errorf("missing entry for controlling profile %q", controller))
			}
		}
		if entry.RunsPhc2Sys {
			runsPhc2Sys = append(runsPhc2Sys, node.profileName)
		}
		result = append(result, verifyNode(node, entry, topology))
	}
	for _, child := range node.children {
		childRuns, childErrors := verifyGraphNode(child, topology, visitState, visitedEntries)
		runsPhc2Sys = append(runsPhc2Sys, childRuns...)
		result = append(result, childErrors...)
	}
	visitState[node] = nodeVisited
	return runsPhc2Sys, result
}

// verify checks each graph node against its clock entry.
func (t ProfileTopology) verify() error {
	var result []error
	var runsPhc2Sys []string
	visitState := make(map[*GraphNode]uint8)
	visitedEntries := make(map[string]bool)
	for _, root := range t.graph {
		rootRuns, rootErrors := verifyGraphNode(root, t, visitState, visitedEntries)
		runsPhc2Sys = append(runsPhc2Sys, rootRuns...)
		result = append(result, rootErrors...)
	}
	for name := range t.entries {
		if !visitedEntries[name] {
			result = append(result, fmt.Errorf("cycle or unreachable profile in graph: %q", name))
		}
	}
	if len(runsPhc2Sys) > 1 {
		result = append(result, fmt.Errorf("multiple processes are configured to run phc2sys: [%s]", strings.Join(runsPhc2Sys, ", ")))
	}
	return errors.Join(result...)
}
