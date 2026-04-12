package ptp4lconf

import (
	"errors"
	"fmt"
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

// Conf represents a parsed ptp4l INI-style configuration
// that can be queried, mutated, and rendered back to text.
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

// Populate parses a ptp4l INI-style config string and optional CLI args,
// populating the Conf struct with sections, options, and inferred clock type.
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
				currentLine := strings.Split(line, "]")
				if len(currentLine) < 2 {
					return errors.New("Section missing closing ']': " + line)
				}
				currentSectionName = fmt.Sprintf("%s]", currentLine[0])
				if currentSectionName != GlobalSectionName && currentSectionName != NmeaSectionName && currentSectionName != UnicastSectionName {
					ifaceCount++
				}
				c.SetOption(currentSectionName, "", "", false)
			} else {
				split := strings.IndexByte(line, ' ')
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

// SectionName strips bracket characters from a section name header,
// returning the bare name (e.g. "[ens1f0]" -> "ens1f0").
func SectionName(name string) string {
	name = strings.ReplaceAll(name, "[", "")
	name = strings.ReplaceAll(name, "]", "")
	return name
}

// RenderOptions renders all options in a section as newline-separated "key value" lines.
func RenderOptions(s Section) string {
	var out string
	for _, opt := range s.Options {
		out = fmt.Sprintf("%s\n%s %s", out, opt.Key, opt.Value)
	}
	return out
}
