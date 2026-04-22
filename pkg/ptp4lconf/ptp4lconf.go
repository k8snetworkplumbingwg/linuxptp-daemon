package ptp4lconf

import (
	"errors"
	"regexp"
	"strings"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
)

// Predefined ptp4l config section names.
const (
	GlobalSectionName  = "[global]"
	NmeaSectionName    = "[nmea]"
	UnicastSectionName = "[unicast_master_table]"
)

var cliArgsSlaveFlagsRegex = regexp.MustCompile(`--(slaveOnly|clientOnly)(\s+1|=1)(\s|$)`)

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

// Conf represents a parsed ptp4l configuration (section-based, space-delimited key-value format).
type Conf struct {
	Sections  []Section
	ClockType event.ClockType
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

// Populate parses a ptp4l config string and optional CLI args into sections/options
// and infers the clock type.
func (c *Conf) Populate(config *string, cliArgs *string) error {
	var currentSectionName string
	c.Sections = make([]Section, 0)
	hasSlaveConfigDefined := false

	if cliArgs != nil {
		args := *cliArgs
		for _, arg := range strings.Fields(args) {
			if arg == "-s" {
				hasSlaveConfigDefined = true
				break
			}
		}
		if cliArgsSlaveFlagsRegex.MatchString(args) {
			hasSlaveConfigDefined = true
		}
	}

	ifaceCount := 0
	if config != nil {
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
					ifaceCount++
				}
				c.SetOption(currentSectionName, "", "", false)
			} else {
				split := strings.IndexAny(line, " \t")
				if split > 0 {
					key := line[:split]
					value := strings.TrimSpace(line[split:])
					c.SetOption(currentSectionName, key, value, false)
					if (key == "masterOnly" && value == "0" && currentSectionName != GlobalSectionName) ||
						(key == "serverOnly" && value == "0") ||
						(key == "slaveOnly" && value == "1") ||
						(key == "clientOnly" && value == "1") {
						hasSlaveConfigDefined = true
					}
				}
			}
		}
	}

	if !hasSlaveConfigDefined {
		c.ClockType = event.GM
	} else if ifaceCount > 1 {
		c.ClockType = event.BC
	} else {
		c.ClockType = event.OC
	}

	return nil
}

// Name returns the section name without brackets (e.g. "[ens1f0]" -> "ens1f0").
func (s Section) Name() string {
	n := strings.ReplaceAll(s.SectionName, "[", "")
	n = strings.ReplaceAll(n, "]", "")
	return n
}
