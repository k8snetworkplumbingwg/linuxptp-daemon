package utils

import (
	"sort"
	"strings"
	"sync"
)

// Aliases instance of the alias manager
var Aliases = &AliasManager{}

// AliasManager ...
type AliasManager struct {
	lock   sync.RWMutex
	values map[string]string
}

func calculateAlias(a []string) string {
	sort.Strings(a)
	return strings.Join(a, "_")
}

// Clear will remove all aliases
func (a *AliasManager) Clear() {
	a.lock.Lock()
	defer a.lock.Unlock()
	a.values = make(map[string]string)
}

// Populate takes an interface collection to populate aliases
func (a *AliasManager) Populate(ifaces map[string][]string) {
	a.lock.Lock()
	defer a.lock.Unlock()

	if a.values == nil {
		a.values = make(map[string]string)
	}
	for _, ifNames := range ifaces {
		alias := calculateAlias(ifNames)
		for _, name := range ifNames {
			a.values[name] = alias
		}
	}
}

// GetAlias returns a interface name and returns the alias
func (a *AliasManager) GetAlias(ifname string) string {
	a.lock.RLock()
	defer a.lock.RUnlock()

	if ifname != "" {
		if alias, ok := a.values[ifname]; ok {
			return alias
		}
	}
	return ifname
}

// GetAlias masks interface names for metric reporting
func GetAlias(ifname string) string {
	return Aliases.GetAlias(ifname)
}
