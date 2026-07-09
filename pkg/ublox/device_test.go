package ublox

import (
	"errors"
	"io/fs"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type mockDirEntry struct {
	name  string
	isDir bool
}

func (m mockDirEntry) Name() string               { return m.name }
func (m mockDirEntry) IsDir() bool                { return m.isDir }
func (m mockDirEntry) Type() fs.FileMode          { return 0 }
func (m mockDirEntry) Info() (fs.FileInfo, error) { return mockFileInfo{name: m.name}, nil }

type mockFileInfo struct{ name string }

func (m mockFileInfo) Name() string       { return m.name }
func (m mockFileInfo) Size() int64        { return 0 }
func (m mockFileInfo) Mode() fs.FileMode  { return 0 }
func (m mockFileInfo) ModTime() time.Time { return time.Time{} }
func (m mockFileInfo) IsDir() bool        { return false }
func (m mockFileInfo) Sys() interface{}   { return nil }

const testSysfsPath = "/sys/class/net/ens7f0/device/gnss"

func setupReadDirMock(entries map[string][]os.DirEntry, errs map[string]error) func() {
	orig := ReadDir
	ReadDir = func(name string) ([]os.DirEntry, error) {
		if errs != nil {
			if err, ok := errs[name]; ok {
				return nil, err
			}
		}
		if entries != nil {
			if e, ok := entries[name]; ok {
				return e, nil
			}
		}
		return nil, errors.New("not found")
	}
	return func() { ReadDir = orig }
}

func TestGNSSDeviceFromInterface(t *testing.T) {
	t.Run("single device", func(t *testing.T) {
		restore := setupReadDirMock(
			map[string][]os.DirEntry{
				testSysfsPath: {mockDirEntry{name: "gnss0"}},
			}, nil,
		)
		defer restore()

		device, err := GNSSDeviceFromInterface("ens7f0")
		assert.NoError(t, err)
		assert.Equal(t, "/dev/gnss0", device)
	})

	t.Run("multiple devices returns first sorted", func(t *testing.T) {
		restore := setupReadDirMock(
			map[string][]os.DirEntry{
				testSysfsPath: {
					mockDirEntry{name: "gnss1"},
					mockDirEntry{name: "gnss0"},
				},
			}, nil,
		)
		defer restore()

		device, err := GNSSDeviceFromInterface("ens7f0")
		assert.NoError(t, err)
		assert.Equal(t, "/dev/gnss0", device)
	})

	t.Run("sysfs directory does not exist", func(t *testing.T) {
		restore := setupReadDirMock(nil,
			map[string]error{
				testSysfsPath: errors.New("no such file or directory"),
			},
		)
		defer restore()

		_, err := GNSSDeviceFromInterface("ens7f0")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no GNSS device found")
	})

	t.Run("sysfs directory is empty", func(t *testing.T) {
		restore := setupReadDirMock(
			map[string][]os.DirEntry{
				testSysfsPath: {},
			}, nil,
		)
		defer restore()

		_, err := GNSSDeviceFromInterface("ens7f0")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "empty")
	})
}
