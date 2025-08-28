package utils

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/golang/glog"
)

// GetAlias the old an deprecated function for masking interfaces
func GetOldAlias(ifname string) string {
	alias := ""
	if ifname != "" {
		// Aliases the interface name or <interface_name>.<vlan>
		dotIndex := strings.Index(ifname, ".")
		if dotIndex == -1 {
			// e.g. ens1f0 -> ens1fx
			alias = ifname[:len(ifname)-1] + "x"
		} else {
			// e.g ens1f0.100 -> ens1fx.100
			alias = ifname[:dotIndex-1] + "x" + ifname[dotIndex:]
		}
	}
	return alias
}

func LookupPCIBusID(ifname string) string {
	base_dir := "/sys/class/net/" + ifname
	path, err := os.Readlink(base_dir + "/device")
	if path == "" || err != nil {
		entries, err := os.ReadDir(base_dir)
		if err != nil {
			return ""
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "lower_") {
				path, err = os.Readlink(base_dir + "/" + entry.Name())
				if err != nil {
					glog.Errorf("failed to find pci bus ID for interface '%s': %s", ifname, err)
					return ""
				}
				list := strings.Split(path, "/")
				return list[len(list)-3]
			}
		}
	}
	return filepath.Base(string(path))
}

// Aliases ...
var Aliases = &AliasManager{}

// AliasManager ...
type AliasManager struct {
	values map[string]string
}

// PopulateBusIDs ...
func (a *AliasManager) PopulateBusIDs(ifNames ...string) {
	if a.values == nil {
		a.values = make(map[string]string)
	}

	for _, ifName := range ifNames {
		busID := LookupPCIBusID(ifName)
		if busID != "" {
			a.values[ifName] = busID[:len(busID)-1] + "x"
		}
	}
}

// Clear ...
func (a *AliasManager) Clear() {
	a.values = make(map[string]string)
}

// GetAlias ...
func (a *AliasManager) GetAlias(ifName string) string {
	if alias, ok := a.values[ifName]; ok {
		return alias
	}
	return ""
}

// GetAlias ...
func GetAlias(ifName string) string {
	return Aliases.GetAlias(ifName)
}
