package alias

import (
	"errors"
	"fmt"
	"strings"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
)

const helpText = "You might want to consider enforcing path or slot based names with systemd udev rules"

// CalculateAliases calculates and sets the aliases.
//
// Expects a map of phcID: [ifname, ...].
// If a unique and singular alias is not found
// for a group or set of groups it will log a warning and
// fall back to the interface name
func CalculateAliases(grouped map[string][]string) {
	errs := make([]error, 0)
	storeInstance.aliases = make(map[string]string)

	seenAliases := make(map[string][]string)
	for phcID, group := range grouped {
		ungroup := false

		// Check groups for any non matching alias
		firstIfAlias := utils.GetAliasValue(group[0])
		storeInstance.aliases[group[0]] = firstIfAlias
		for _, i := range group[1:] {
			alias := utils.GetAliasValue(i)
			storeInstance.aliases[i] = alias

			if firstIfAlias != alias {
				ungroup = true
				errs = append(errs, fmt.Errorf(
					"one or more interfaces in group ['%s'] does not alias to a common value, falling back to interface names. %s",
					strings.Join(group, "', '"),
					helpText,
				))
				break
			}
		}

		// If non-matching alias is found, do not alias any of the group
		if ungroup {
			for _, i := range group {
				storeInstance.aliases[i] = i
			}
		} else {
			seenAliases[firstIfAlias] = append(seenAliases[firstIfAlias], phcID)
		}
	}

	// Check if PHCs have matching aliases
	for _, phcIDs := range seenAliases {
		if len(phcIDs) > 1 {
			names := make([]string, 0)
			for _, pid := range phcIDs {
				if group, ok := grouped[pid]; ok {
					names = append(names, group...)
					for _, i := range group {
						storeInstance.aliases[i] = i
					}
				}
			}
			errs = append(errs, fmt.Errorf(
				"one or more PHC IDs have the same alias ['%s'] falling back to interface names. %s",
				strings.Join(names, "', '"),
				helpText,
			))
		}
	}

	if len(errs) > 0 {
		glog.Warning(errors.Join(errs...))
	}
}

// GetAlias ...
func GetAlias(ifname string) string {
	return storeInstance.getAlias(ifname)
}

// SetAlias ...
func SetAlias(ifname, alias string) {
	storeInstance.setAlias(ifname, alias)
}

// ClearAliases ...
func ClearAliases() {
	storeInstance.clear()
}

// Debug ...
func Debug(logF func(string, ...any)) {
	for ifName, alias := range storeInstance.aliases {
		logF("DEBUG: ifname: '%s' alias: '%s'\n", ifName, alias)
	}
}
