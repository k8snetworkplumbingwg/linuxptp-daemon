package intel

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_applyDevicePins(t *testing.T) {
	mfs, restoreFS := setupMockFS()
	defer restoreFS()

	phcEntries := []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}
	mfs.AllowReadDir("/sys/class/net/eth0/device/ptp/", phcEntries, nil)
	mfs.AllowWriteFile("/sys/class/net/eth0/device/ptp/ptp0/pins/PIN")

	mfs.AllowReadDir("/sys/class/net/bad1/device/ptp/", nil, errors.New("ReadDir failure"))

	phcEntries = append(phcEntries, MockDirEntry{name: "ptp1", isDir: true})
	mfs.AllowReadDir("/sys/class/net/eth1/device/ptp/", phcEntries, nil)
	mfs.AllowWriteFile("/sys/class/net/eth1/device/ptp/ptp0/pins/PIN")
	mfs.AllowWriteFile("/sys/class/net/eth1/device/ptp/ptp1/pins/PIN")

	devicePins := DevicePinConfig{
		"eth0": {
			"PIN": "0 0",
			"BAD": "ignored",
		},
		"bad1": {
			"PIN": "ignored",
		},
		"eth1": {
			"PIN": "1 1",
		},
	}
	applyDevicePins(devicePins)
	// Ensure all 3 expected writes occurred
	assert.Contains(t, mfs.allowedReadFile, "/sys/class/net/eth0/device/ptp/ptp0/pins/PIN")
	assert.Contains(t, mfs.allowedReadFile, "/sys/class/net/eth1/device/ptp/ptp0/pins/PIN")
	assert.Contains(t, mfs.allowedReadFile, "/sys/class/net/eth1/device/ptp/ptp1/pins/PIN")
	// Ensure the 2 bad writes did not occur
	assert.NotContains(t, mfs.allowedReadFile, "/sys/class/net/eth0/device/ptp/ptp0/pins/BAD")
	assert.NotContains(t, mfs.allowedReadFile, "/sys/class/net/bad/device/ptp/ptp0/pins/PIN")
}
