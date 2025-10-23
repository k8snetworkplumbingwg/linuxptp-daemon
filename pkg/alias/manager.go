package alias

type store struct {
	aliases map[string]string
}

func (m *store) getAlias(ifName string) string {
	if v, ok := m.aliases[ifName]; ok {
		return v
	}
	return ifName
}

func (m *store) setAlias(ifName, alias string) {
	m.aliases[ifName] = alias
}

func (m *store) clear() {
	m.aliases = make(map[string]string)
}

var storeInstance = store{aliases: map[string]string{}}
