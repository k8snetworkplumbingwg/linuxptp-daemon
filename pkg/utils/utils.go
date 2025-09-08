package utils

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/golang/glog"
)

// GetOldAlias the old an deprecated function for masking interfaces
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

func lookupPCIBusID(ifname string) string {
	baseDir := "/sys/class/net/" + ifname
	path, err := FileSystem.Readlink(baseDir + "/device")
	if err != nil || path == "" {
		entries, err2 := FileSystem.ReadDir(baseDir)
		if err2 != nil {
			return ""
		}
		slices.SortFunc(entries, func(a, b os.DirEntry) int {
			return strings.Compare(a.Name(), b.Name())
		})
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "lower_") {
				path, err = FileSystem.Readlink(baseDir + "/" + entry.Name())
				if err != nil {
					glog.Errorf("failed to find pci bus ID for interface '%s': %s", ifname, err)
					continue
				}
				list := strings.Split(path, "/")
				if len(list) < 3 {
					continue
				}
				return list[len(list)-3]
			}
		}
		glog.Errorf("failed to find pci bus ID for interface '%s'", ifname)
		return ""
	}
	return filepath.Base(path)
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
		busID := lookupPCIBusID(ifName)
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
	return ifName
}

// GetAlias ...
func GetAlias(ifName string) string {
	return Aliases.GetAlias(ifName)
}
