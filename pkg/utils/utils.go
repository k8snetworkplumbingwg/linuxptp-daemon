package utils

import (
	"sort"
	"strings"
)

var Aliases = &AliasManager{}

type AliasManager struct {
	values map[string]string
}

type aliasedList []string

func (a aliasedList) GetAlias() string {
	sort.Strings(a)
	return strings.Join(a, "_")
}

type iFace interface {
	GetName() string
	GetPhcID() string
}

type iFaceCollection interface {
	GetIfnamesForPhcID(phcId string) []string
	GetPhcIDs() []string
}

func (a *AliasManager) Populate(ifaces iFaceCollection) {
	if a.values == nil {
		a.values = make(map[string]string)
	}
	groupedByPhc := make(map[string]aliasedList)
	for _, phcId := range ifaces.GetPhcIDs() {
		ifaces := ifaces.GetIfnamesForPhcID(phcId)
		groupedByPhc[phcId] = make(aliasedList, len(ifaces))
		copy(groupedByPhc[phcId], ifaces)
	}
	for phcId, ifaces := range groupedByPhc {
		a.values[phcId] = ifaces.GetAlias()
	}
}

func (a *AliasManager) GetAlias(ifname string) string {
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
