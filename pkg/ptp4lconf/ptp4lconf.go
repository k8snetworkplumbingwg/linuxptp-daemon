package ptp4lconf

import (
	"errors"
	"strings"
)

// Predefined ptp4l config section names.
const (
	GlobalSectionName  = "[global]"
	NmeaSectionName    = "[nmea]"
	UnicastSectionName = "[unicast_master_table]"
)

// Option is a single key-value pair within a ptp4l config section.
type Option struct {
	Key   string
	Value string
}

// Section is a named group of options in a ptp4l INI-style config (e.g. "[global]").
type Section struct {
	SectionName string
	Options     []Option
}

// PortRoleSummary counts how many interface ports have explicit
// master/server or slave/client role settings.
// "Default" means the key was absent for that port.
type PortRoleSummary struct {
	MasterOnlyTrue    int // ports with masterOnly=1 or serverOnly=1
	MasterOnlyFalse   int // ports with masterOnly=0 or serverOnly=0
	MasterOnlyDefault int // ports with no masterOnly/serverOnly set
	SlaveOnlyTrue     int // ports with slaveOnly=1 or clientOnly=1
	SlaveOnlyFalse    int // ports with slaveOnly=0 or clientOnly=0
	SlaveOnlyDefault  int // ports with no slaveOnly/clientOnly set
	TotalPorts        int // total interface sections (excludes global, nmea, unicast)
}

// Conf represents a parsed ptp4l configuration (section-based, space-delimited key-value format).
type Conf struct {
	Sections  []Section
	PortRoles PortRoleSummary
}

// GetOption returns the value for a given key in the named section.
// The bool indicates whether the key was found.
func (c *Conf) GetOption(sectionName, key string) (string, bool) {
	for _, section := range c.Sections {
		if section.SectionName == sectionName {
			for _, opt := range section.Options {
				if opt.Key == key {
					return opt.Value, true
				}
			}
		}
	}
	return "", false
}

// SetOption inserts or (if overwrite is true) updates an option in the named section.
// If the section does not exist it is created. If key is empty, only the section is created.
func (c *Conf) SetOption(sectionName, key, value string, overwrite bool) {
	var updatedSection Section
	index := -1
	for i, section := range c.Sections {
		if section.SectionName == sectionName {
			updatedSection = section
			index = i
		}
	}
	if index < 0 {
		updatedSection = Section{Options: make([]Option, 0), SectionName: sectionName}
		index = len(c.Sections)
		c.Sections = append(c.Sections, updatedSection)
	}

	if key == "" {
		return
	}

	found := false
	if overwrite {
		for i := range updatedSection.Options {
			if updatedSection.Options[i].Key == key {
				updatedSection.Options[i] = Option{Key: key, Value: value}
				found = true
			}
		}
	}
	if !found {
		updatedSection.Options = append(updatedSection.Options, Option{Key: key, Value: value})
	}
	c.Sections[index] = updatedSection
}

// Populate parses a ptp4l config string into sections/options
// and collects per-port role summary counts.
func (c *Conf) Populate(config *string) error {
	var currentSectionName string
	c.Sections = make([]Section, 0)
	c.PortRoles = PortRoleSummary{}

	if config == nil {
		return nil
	}

	type portFlags struct {
		hasMaster bool
		hasSlave  bool
	}
	portFlagMap := make(map[string]*portFlags)

	for _, line := range strings.Split(*config, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		} else if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return errors.New("Section missing closing ']': " + line)
			}
			currentSectionName = line
			if currentSectionName != GlobalSectionName && currentSectionName != NmeaSectionName && currentSectionName != UnicastSectionName {
				c.PortRoles.TotalPorts++
				portFlagMap[currentSectionName] = &portFlags{}
			}
			c.SetOption(currentSectionName, "", "", false)
		} else {
			split := strings.IndexAny(line, " \t")
			if split > 0 {
				key := line[:split]
				value := strings.TrimSpace(line[split:])
				c.SetOption(currentSectionName, key, value, false)

				pf, isPort := portFlagMap[currentSectionName]
				if !isPort {
					// global-level slaveOnly/clientOnly still counts
					if currentSectionName == GlobalSectionName {
						if (key == "slaveOnly" && value == "1") || (key == "clientOnly" && value == "1") {
							c.PortRoles.SlaveOnlyTrue++
						}
					}
					continue
				}

				switch {
				case (key == "masterOnly" || key == "serverOnly") && value == "1":
					c.PortRoles.MasterOnlyTrue++
					pf.hasMaster = true
				case (key == "masterOnly" || key == "serverOnly") && value == "0":
					c.PortRoles.MasterOnlyFalse++
					pf.hasMaster = true
				case (key == "slaveOnly" || key == "clientOnly") && value == "1":
					c.PortRoles.SlaveOnlyTrue++
					pf.hasSlave = true
				case (key == "slaveOnly" || key == "clientOnly") && value == "0":
					c.PortRoles.SlaveOnlyFalse++
					pf.hasSlave = true
				}
			}
		}
	}

	for _, pf := range portFlagMap {
		if !pf.hasMaster {
			c.PortRoles.MasterOnlyDefault++
		}
		if !pf.hasSlave {
			c.PortRoles.SlaveOnlyDefault++
		}
	}

	return nil
}

// Name returns the section name without brackets (e.g. "[ens1f0]" -> "ens1f0").
func (s Section) Name() string {
	n := strings.ReplaceAll(s.SectionName, "[", "")
	n = strings.ReplaceAll(n, "]", "")
	return n
}
