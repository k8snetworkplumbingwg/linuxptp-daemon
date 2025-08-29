package utils

import (
	"fmt"
	"os"
	"regexp"
)

// FileSystem ...
var FileSystem ReadlinkFS = OSReadlinkFS{}

// ReadlinkFS ...
type ReadlinkFS interface {
	Readlink(name string) (string, error)
	ReadDir(name string) ([]os.DirEntry, error)
}

// OSReadlinkFS ...
type OSReadlinkFS struct {
}

// Readlink ...
func (fs OSReadlinkFS) Readlink(name string) (string, error) {
	return os.Readlink(name)
}

// ReadDir ...
func (fs OSReadlinkFS) ReadDir(name string) ([]os.DirEntry, error) {
	return os.ReadDir(name)
}

// MockedDirEntry ...
type MockedDirEntry struct {
	NameValue  string
	IsDirValue bool
	TypeValue  os.FileMode
	InfoValue  os.FileInfo
}

// Name ...
func (d MockedDirEntry) Name() string {
	return d.NameValue
}

// IsDir ...
func (d MockedDirEntry) IsDir() bool {
	return d.IsDirValue
}

// Type ...
func (d MockedDirEntry) Type() os.FileMode {
	return d.TypeValue
}

// Info ...
func (d MockedDirEntry) Info() (os.FileInfo, error) {
	return d.InfoValue, nil
}

// MockedReadlinkFS ...
type MockedReadlinkFS struct {
	ReadLinkValues map[string]string
	ReadDirValues  map[string][]os.DirEntry
}

var devicePath = regexp.MustCompile(`/sys/class/net/([^/]+)/device|/sys/class/net/[^/]+/lower_([^/]+)`)

// Readlink ...
func (mock *MockedReadlinkFS) Readlink(path string) (string, error) {
	name := ""
	if match := devicePath.FindStringSubmatch(path); len(match) > 0 {
		name = match[1]
	} else {
		return "", fmt.Errorf("file not found")
	}
	if val, ok := mock.ReadLinkValues[name]; ok {
		return val, nil
	}
	return "", fmt.Errorf("file not found")
}

// ReadDir ...
func (mock *MockedReadlinkFS) ReadDir(name string) ([]os.DirEntry, error) {
	return mock.ReadDirValues[name], nil
}
