package ublox

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/golang/glog"
)

const (
	// GNSSDeviceSysfsTemplate is the sysfs path template for finding GNSS
	// devices attached to a network interface.
	GNSSDeviceSysfsTemplate = "/sys/class/net/%s/device/gnss"

	// ttyClassSysfsPath contains the sysfs class entries for tty devices.
	ttyClassSysfsPath = "/sys/class/tty"
)

// ReadDir is the function used to read sysfs directories.
// Replace in tests to mock filesystem access.
var ReadDir = os.ReadDir

// GNSSDeviceFromInterface resolves the GNSS TTY device path for a given
// network interface by reading the sysfs directory
// /sys/class/net/<iface>/device/gnss/.
func GNSSDeviceFromInterface(iface string) (string, error) {
	glog.Infof("Looking for GNSS device associated with iface %s", iface)
	gnssDir := fmt.Sprintf(GNSSDeviceSysfsTemplate, iface)
	entries, err := ReadDir(gnssDir)
	if err != nil {
		return "", fmt.Errorf("no GNSS device found for interface %s: %w", iface, err)
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("GNSS sysfs directory %s is empty", gnssDir)
	}
	if len(entries) > 1 {
		// Sort for deterministic selection when multiple devices exist
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Name() < entries[j].Name()
		})
		glog.Warningf("multiple GNSS devices found for %s, using %s", iface, entries[0].Name())
	}
	result := fmt.Sprintf("/dev/%s", entries[0].Name())
	glog.Infof("Detected GNSS device %s", result)
	return result, nil
}

// GNSSDeviceFromUSB resolves the tty device exposed by a USB device with the
// given hexadecimal vendor and product IDs. It enumerates tty class devices
// and walks their sysfs ancestry, rather than relying on an unstable tty name
// such as ttyACM0 or ttyUSB0.
func GNSSDeviceFromUSB(vendor, product string) (string, error) {
	rawVendor, rawProduct := vendor, product
	vendor, err := normalizeUSBID(vendor)
	if err != nil {
		return "", fmt.Errorf("invalid USB vendor ID %q: %w", rawVendor, err)
	}
	product, err = normalizeUSBID(product)
	if err != nil {
		return "", fmt.Errorf("invalid USB product ID %q: %w", rawProduct, err)
	}

	return findTTYFromUSBDevice(ttyClassSysfsPath, vendor, product)
}

func findTTYFromUSBDevice(ttyClassPath, vendor, product string) (string, error) {
	glog.Infof("Looking for GNSS tty exposed by USB device %s:%s", vendor, product)
	entries, err := ReadDir(ttyClassPath)
	if err != nil {
		return "", fmt.Errorf("cannot enumerate tty devices: %w", err)
	}

	var candidates []string
	for _, entry := range entries {
		devicePath := filepath.Join(ttyClassPath, entry.Name(), "device")
		resolvedDevicePath, err := filepath.EvalSymlinks(devicePath)
		if err != nil {
			// Virtual tty devices and stale class entries may not have a
			// resolvable device path.
			continue
		}
		if usbDeviceMatches(resolvedDevicePath, vendor, product) {
			candidates = append(candidates, filepath.Join("/dev", entry.Name()))
		}
	}

	sort.Strings(candidates)
	switch len(candidates) {
	case 0:
		return "", fmt.Errorf("no tty device found for USB device %s:%s", vendor, product)
	case 1:
		glog.Infof("Detected GNSS device %s", candidates[0])
		return candidates[0], nil
	default:
		return "", fmt.Errorf("multiple tty devices found for USB device %s:%s: %s",
			vendor, product, strings.Join(candidates, ", "))
	}
}

// usbDeviceMatches walks from a tty's sysfs device to its parents, looking for
// a USB device node with matching idVendor and idProduct attributes.
func usbDeviceMatches(devicePath, vendor, product string) bool {
	for path := devicePath; path != "." && path != string(filepath.Separator); path = filepath.Dir(path) {
		subsystemPath, err := filepath.EvalSymlinks(filepath.Join(path, "subsystem"))
		if err == nil && filepath.Base(subsystemPath) == "usb" {
			actualVendor, vendorErr := os.ReadFile(filepath.Join(path, "idVendor"))
			actualProduct, productErr := os.ReadFile(filepath.Join(path, "idProduct"))
			if vendorErr == nil && productErr == nil &&
				strings.EqualFold(strings.TrimSpace(string(actualVendor)), vendor) &&
				strings.EqualFold(strings.TrimSpace(string(actualProduct)), product) {
				return true
			}
		}
	}
	return false
}

func normalizeUSBID(id string) (string, error) {
	id = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(id), "0x"))
	if id == "" || len(id) > 4 {
		return "", fmt.Errorf("must be one to four hexadecimal digits")
	}
	value, err := strconv.ParseUint(id, 16, 16)
	if err != nil {
		return "", fmt.Errorf("must be hexadecimal: %w", err)
	}
	return fmt.Sprintf("%04x", value), nil
}
