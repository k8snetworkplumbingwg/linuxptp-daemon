package version

import (
	"fmt"
	"strconv"

	"github.com/golang/glog"
)

// These are the package version we have available
// "3.1.1-2.el8_6.3"
// "3.1.1-6.el9_2.7"
// "4.2-2.el9_4.3"
// "4.4-1.el9"

// Version ...
type Version struct {
	major   int
	minor   int
	patch   int
	release suffix
}

type suffix struct {
	leading     int
	repoPrefix  string
	repoVersion int
	trailing    []int
}

// MustParse ...
func MustParse(versionStr string) Version {
	v, err := parse(versionStr)
	if err != nil {
		glog.Fatalf("Failed to parse version: %s", versionStr)
	}
	return v
}

func parse(versionStr string) (Version, error) {
	v := Version{}

	v.major, versionStr = takeNumbers(versionStr, true)
	if v.major == 0 {
		return v, fmt.Errorf("malformed version")
	}

	v.minor, versionStr = takeNumbers(versionStr, false)
	if v.minor == 0 {
		return v, fmt.Errorf("malformed version")
	}

	if string(versionStr[0]) == "." {
		v.patch, versionStr = takeNumbers(versionStr[1:], false)
	}

	if versionStr != "" {
		if string(versionStr[0]) != "-" {
			return v, fmt.Errorf("malformed version")
		}
		var err error
		v.release, versionStr, err = parseSuffix(versionStr[1:])
		if err != nil {
			return v, fmt.Errorf("malformed version: %w", err)
		}
	}

	if versionStr != "" {
		return v, fmt.Errorf("malformed version")
	}
	return v, nil
}

func parseSuffix(versionStr string) (suffix, string, error) {
	s := suffix{}

	s.leading, versionStr = takeNumbers(versionStr, true)
	if s.leading == 0 {
		return s, versionStr, fmt.Errorf("malformed release version")
	}
	s.repoPrefix, versionStr = takeLetters(versionStr, false)
	if s.repoPrefix == "" {
		return s, versionStr, fmt.Errorf("malformed release version")
	}

	s.repoVersion, versionStr = takeNumbers(versionStr, true)
	if s.repoVersion == 0 {
		return s, versionStr, fmt.Errorf("malformed release version")
	}

	var num int
	for versionStr != "" {
		num, versionStr = takeNumbers(versionStr, true)
		s.trailing = append(s.trailing, num)
	}
	return s, versionStr, nil
}

func isDigit(r rune) bool {
	return r >= '0' && r <= '9'
}

func isSep(r rune) bool {
	return r == '.' || r == '-' || r == '_'
}

func takeLetters(versionStr string, stripTrailing bool) (string, string) {
	taken := ""
	index := 0
	for i, r := range versionStr {
		index = i
		if isDigit(r) || isSep(r) {
			break
		}
		taken += string(r)
	}
	if taken == "" {
		return "", versionStr
	}

	if stripTrailing {
		index++
	}

	return taken, versionStr[index:]
}

func takeNumbers(versionStr string, strip bool) (int, string) {
	taken := ""
	index := 0
	for i, r := range versionStr {
		index = i
		if isSep(r) || !isDigit(r) {
			break
		}
		taken += string(r)
	}
	if taken == "" {
		return 0, versionStr
	}

	// Composed of only digit so unlikely to have issue
	v, _ := strconv.Atoi(taken)

	if strip {
		index++
	}

	return v, versionStr[index:]
}

// String ...
func (v Version) String() string {
	vStr := fmt.Sprintf("%d.%d", v.major, v.minor)
	if v.patch != 0 {
		vStr += fmt.Sprintf(".%d", v.patch)
	}
	vStr += v.release.String()
	return vStr
}

// String ...
func (s suffix) String() string {
	vStr := ""
	if s.leading != 0 {
		vStr += fmt.Sprintf("%d.", s.leading)
	}
	if s.repoPrefix != "" {
		vStr += s.repoPrefix
	}
	if s.repoVersion != 0 {
		vStr += fmt.Sprint(s.repoVersion)
	}
	if len(s.trailing) != 0 {
		vStr += "_"
	}
	for i, n := range s.trailing {
		if i != 0 {
			vStr += "."
		}
		vStr += fmt.Sprintf("%d", n)
	}
	if vStr != "" {
		vStr = "-" + vStr
	}
	return vStr
}

// Compare ...
//
// Returns
//
//	-1 if the version if larger than the supplied argument
//	0 if the version both are the same
//	1 if the version if smaller than the supplied argument
func (v Version) Compare(other Version) int {
	if v.major > other.major {
		return -1
	} else if v.major < other.major {
		return 1
	}

	if v.minor > other.minor {
		return -1
	} else if v.minor < other.minor {
		return 1
	}

	if v.patch > other.patch {
		return -1
	} else if v.patch < other.patch {
		return 1
	}

	return v.release.Compare(other.release)
}

func (s suffix) Compare(other suffix) int {
	if s.leading > other.leading {
		return -1
	} else if s.leading < other.leading {
		return 1
	}

	// Should we compare strings in this way?
	if s.repoPrefix > other.repoPrefix {
		return -1
	} else if s.repoPrefix < other.repoPrefix {
		return 1
	}

	if s.repoVersion > other.repoVersion {
		return -1
	} else if s.repoVersion < other.repoVersion {
		return 1
	}

	nTrailing := min(len(s.trailing), len(other.trailing))

	for i := range nTrailing {
		if s.trailing[i] > other.trailing[i] {
			return -1
		} else if s.trailing[i] < other.trailing[i] {
			return 1
		}
	}

	if len(s.trailing) > nTrailing {
		if noneZeroCount(s.trailing[nTrailing:]) > 0 {
			return -1
		}
	} else if len(other.trailing) > nTrailing {
		if noneZeroCount(other.trailing[nTrailing:]) > 0 {
			return 1
		}
	}
	return 0
}

func noneZeroCount(seq []int) int {
	sNoneZeroLength := 0
	for _, n := range seq {
		if n != 0 {
			sNoneZeroLength++
		}
	}
	return sNoneZeroLength
}
