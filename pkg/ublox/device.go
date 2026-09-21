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

	// netClassSysfsPath contains the sysfs class entries for network devices.
	netClassSysfsPath = "/sys/class/net"

	// pciNetSysfsTemplate locates network interfaces belonging to a PCI device.
	pciNetSysfsTemplate = "/sys/bus/pci/devices/%s/net"
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

// GNSSDeviceFromEthernetDevice resolves a GNSS device attached to an Ethernet
// device. When name is provided, it is used directly. Otherwise, PCI slot,
// vendor, and device ID are used to discover the interface through sysfs.
// All matching criteria other than name are applied together, and the result
// must identify exactly one GNSS device.
func GNSSDeviceFromEthernetDevice(name, pciSlot, vendor, deviceID string) (string, error) {
	if name != "" {
		return GNSSDeviceFromInterface(name)
	}

	if pciSlot == "" && vendor == "" && deviceID == "" {
		return "", fmt.Errorf("EthernetDevice has no selection criteria")
	}

	if vendor != "" {
		rawVendor := vendor
		var err error
		vendor, err = normalizePCIID(vendor)
		if err != nil {
			return "", fmt.Errorf("invalid PCI vendor ID %q: %w", rawVendor, err)
		}
	}
	if deviceID != "" {
		rawDeviceID := deviceID
		var err error
		deviceID, err = normalizePCIID(deviceID)
		if err != nil {
			return "", fmt.Errorf("invalid PCI device ID %q: %w", rawDeviceID, err)
		}
	}

	var interfaces []string
	var err error
	if pciSlot != "" {
		pciSlot, err = normalizePCISlot(pciSlot)
		if err != nil {
			return "", err
		}
	}
	switch {
	case pciSlot != "":
		interfaces, err = ethernetInterfacesFromPCISlot(pciSlot, vendor, deviceID)
	default:
		interfaces, err = ethernetInterfacesFromPCIIDs(vendor, deviceID)
	}
	if err != nil {
		return "", err
	}
	return gnssDeviceFromInterfaces(interfaces, "EthernetDevice")
}

func ethernetInterfacesFromPCISlot(pciSlot, vendor, deviceID string) ([]string, error) {
	if filepath.Base(pciSlot) != pciSlot || pciSlot == "." || pciSlot == ".." {
		return nil, fmt.Errorf("invalid PCI slot %q", pciSlot)
	}

	netDir := fmt.Sprintf(pciNetSysfsTemplate, pciSlot)
	entries, err := ReadDir(netDir)
	if err != nil {
		return nil, fmt.Errorf("no Ethernet device found for PCI slot %s: %w", pciSlot, err)
	}

	interfaces := make([]string, 0, len(entries))
	for _, entry := range entries {
		if vendor == "" && deviceID == "" || pciInterfaceMatches(entry.Name(), vendor, deviceID) {
			interfaces = append(interfaces, entry.Name())
		}
	}
	return interfaces, nil
}

func ethernetInterfacesFromPCIIDs(vendor, deviceID string) ([]string, error) {
	entries, err := ReadDir(netClassSysfsPath)
	if err != nil {
		return nil, fmt.Errorf("cannot enumerate Ethernet devices: %w", err)
	}

	interfaces := make([]string, 0, len(entries))
	for _, entry := range entries {
		if pciInterfaceMatches(entry.Name(), vendor, deviceID) {
			interfaces = append(interfaces, entry.Name())
		}
	}
	return interfaces, nil
}

func pciInterfaceMatches(iface, vendor, deviceID string) bool {
	devicePath, err := filepath.EvalSymlinks(filepath.Join(netClassSysfsPath, iface, "device"))
	if err != nil {
		return false
	}

	if vendor != "" {
		actual, err := os.ReadFile(filepath.Join(devicePath, "vendor"))
		if err != nil || !strings.EqualFold(strings.TrimSpace(string(actual)), "0x"+vendor) {
			return false
		}
	}
	if deviceID != "" {
		actual, err := os.ReadFile(filepath.Join(devicePath, "device"))
		if err != nil || !strings.EqualFold(strings.TrimSpace(string(actual)), "0x"+deviceID) {
			return false
		}
	}
	return true
}

func gnssDeviceFromInterfaces(interfaces []string, selector string) (string, error) {
	sort.Strings(interfaces)
	var candidates []string
	for _, iface := range interfaces {
		device, err := GNSSDeviceFromInterface(iface)
		if err == nil {
			candidates = append(candidates, device)
		}
	}

	switch len(candidates) {
	case 0:
		return "", fmt.Errorf("no GNSS device found for %s", selector)
	case 1:
		return candidates[0], nil
	default:
		return "", fmt.Errorf("multiple GNSS devices found for %s: %s", selector, strings.Join(candidates, ", "))
	}
}

// GNSSDeviceFromACPIDevice resolves the tty exposed by an ACPI-enumerated
// serial controller. It walks each tty's sysfs ancestry and matches an ACPI
// device name such as INTC10EE:00, rather than relying on the dynamically
// assigned tty name (for example, ttyS2).
func GNSSDeviceFromACPIDevice(hid, uid string) (string, error) {
	hid = strings.TrimSpace(hid)
	uid = strings.TrimSpace(uid)
	if hid == "" {
		return "", fmt.Errorf("invalid ACPI hardware ID: must not be empty")
	}
	return findTTYFromACPIDevice(ttyClassSysfsPath, hid, uid)
}

func findTTYFromACPIDevice(ttyClassPath, hid, uid string) (string, error) {
	selector := hid
	if uid != "" {
		selector += ":" + uid
	}
	glog.Infof("Looking for GNSS tty exposed by ACPI serial device %s", selector)
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
		if acpiDeviceMatches(resolvedDevicePath, hid, uid) {
			candidates = append(candidates, filepath.Join("/dev", entry.Name()))
		}
	}

	sort.Strings(candidates)
	switch len(candidates) {
	case 0:
		return "", fmt.Errorf("no tty device found for ACPI serial device %s", selector)
	case 1:
		glog.Infof("Detected GNSS device %s", candidates[0])
		return candidates[0], nil
	default:
		return "", fmt.Errorf("multiple tty devices found for ACPI serial device %s: %s",
			selector, strings.Join(candidates, ", "))
	}
}

// acpiDeviceMatches walks from a tty's sysfs device to its parents, looking
// for an ACPI device directory named <HID>:<UID>. If uid is empty, any
// instance of the requested HID matches.
func acpiDeviceMatches(devicePath, hid, uid string) bool {
	for path := devicePath; path != "." && path != string(filepath.Separator); path = filepath.Dir(path) {
		name := filepath.Base(path)
		parts := strings.SplitN(name, ":", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], hid) {
			continue
		}
		if uid == "" || strings.EqualFold(parts[1], uid) {
			return true
		}
	}
	return false
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
	return normalizePCIID(id)
}

func normalizePCISlot(slot string) (string, error) {
	slot = strings.TrimSpace(slot)
	if filepath.Base(slot) != slot || slot == "." || slot == ".." {
		return "", fmt.Errorf("invalid PCI slot %q", slot)
	}
	if strings.Count(slot, ":") == 1 {
		slot = "0000:" + slot
	}
	return slot, nil
}

func normalizePCIID(id string) (string, error) {
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
