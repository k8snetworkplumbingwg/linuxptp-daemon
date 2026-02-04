package network

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/golang/glog"
	"github.com/jaypipes/ghw"
)

const (
	_ETHTOOL_HARDWARE_RECEIVE_CAP   = "hardware-receive"
	_ETHTOOL_HARDWARE_TRANSMIT_CAP  = "hardware-transmit"
	_ETHTOOL_HARDWARE_RAW_CLOCK_CAP = "hardware-raw-clock"
)

// VPD parsing constants (PCI Local Bus Specification)
const (
	pciVPDIDStringTag        = 0x82
	pciVPDROTag              = 0x90
	pciVPDEndTag             = 0x78
	pciVPDBlockDescriptorLen = 3
	pciVPDKeywordLen         = 2
)

// EthtoolInfo holds driver and firmware information from ethtool -i
type EthtoolInfo struct {
	Driver          string
	DriverVersion   string
	FirmwareVersion string
	BusInfo         string
	ExpansionRom    string
}

// LinkInfo holds link status, speed, and FEC information
type LinkInfo struct {
	LinkDetected bool
	Speed        string // e.g., "25000Mb/s" or "Unknown!"
	Duplex       string // e.g., "Full"
	FEC          string // e.g., "RS", "BaseR", "Off", "None"
}

// VPDInfo holds Vital Product Data parsed from device VPD
type VPDInfo struct {
	IdentifierString string
	PartNumber       string
	SerialNumber     string
	ManufacturerID   string
	VendorSpecific1  string // V1 field - often contains product info
	VendorSpecific2  string // V2 field
	ProductName      string // V0 field
}

func ethtoolInstalled() bool {
	_, err := exec.LookPath("ethtool")
	return err == nil
}

func netParseEthtoolTimeStampFeature(cmdOut *bytes.Buffer) bool {
	var hardRxEnabled bool
	var hardTxEnabled bool
	var hardRawEnabled bool

	// glog.V(2).Infof("cmd output for %v", cmdOut)
	scanner := bufio.NewScanner(cmdOut)
	for scanner.Scan() {
		line := strings.TrimPrefix(scanner.Text(), "\t")
		parts := strings.Fields(line)
		if parts[0] == _ETHTOOL_HARDWARE_RECEIVE_CAP {
			hardRxEnabled = true
		}
		if parts[0] == _ETHTOOL_HARDWARE_TRANSMIT_CAP {
			hardTxEnabled = true
		}
		if parts[0] == _ETHTOOL_HARDWARE_RAW_CLOCK_CAP {
			hardRawEnabled = true
		}
	}
	return hardRxEnabled && hardTxEnabled && hardRawEnabled
}

func DiscoverPTPDevices() ([]string, error) {
	var out bytes.Buffer
	nics := make([]string, 0)

	if !ethtoolInstalled() {
		return nics, fmt.Errorf("discoverDevices(): ethtool not installed. Cannot grab NIC capabilities")
	}

	ethtoolPath, _ := exec.LookPath("ethtool")

	net, err := ghw.Network()
	if err != nil {
		return nics, fmt.Errorf("discoverDevices(): error getting network info: %v", err)
	}

	for _, dev := range net.NICs {
		// glog.Infof("grabbing NIC timestamp capability for %v", dev.Name)
		cmd := exec.Command(ethtoolPath, "-T", dev.Name)
		cmd.Stdout = &out
		err := cmd.Run()
		if err != nil {
			glog.Infof("could not grab NIC timestamp capability for %v: %v", dev.Name, err)
		}

		if !netParseEthtoolTimeStampFeature(&out) {
			glog.Infof("Skipping NIC %v as it does not support HW timestamping", dev.Name)
			continue
		}

		if dev.PCIAddress == nil {
			glog.Warningf("Skipping NIC %v as it does not have a PCI address", dev.Name)
			continue
		}

		// If the physfn doesn't exist this means the interface is not a virtual function so we ca add it to the list
		if _, err = os.Stat(fmt.Sprintf("/sys/bus/pci/devices/%s/physfn", *dev.PCIAddress)); os.IsNotExist(err) {
			nics = append(nics, dev.Name)
		}
	}
	return nics, nil
}

func GetPhcId(iface string) string {
	var err error
	var id int
	if id, err = getPTPClockIndex(iface); err != nil {
		glog.Error(err.Error())
	} else {
		return fmt.Sprintf("/dev/ptp%d", id)
	}
	return ""
}

func getPTPClockIndex(iface string) (int, error) {
	if !ethtoolInstalled() {
		return 0, fmt.Errorf("discoverDevices(): ethtool not installed")
	}
	out, err := exec.Command("ethtool", "-T", iface).CombinedOutput()
	if err != nil {
		return -1, fmt.Errorf("failed to run ethtool: %w", err)
	}

	// Try classic format
	if m := regexp.MustCompile(`PTP Hardware Clock:\s*(\d+)`).FindSubmatch(out); m != nil {
		var idx int
		_, err = fmt.Sscan(string(m[1]), &idx)
		return idx, err
	}

	// Try provider index format (seen in some containers)
	if m := regexp.MustCompile(`Hardware timestamp provider index:\s*(\d+)`).FindSubmatch(out); m != nil {
		var idx int
		_, err = fmt.Sscan(string(m[1]), &idx)
		return idx, err
	}

	// Sysfs fallback
	matches, err := filepath.Glob(fmt.Sprintf("/sys/class/net/%s/ptp/ptp*", iface))
	if err == nil && len(matches) > 0 {
		base := filepath.Base(matches[0]) // e.g., "ptp0"
		if strings.HasPrefix(base, "ptp") {
			var idx int
			_, err = fmt.Sscan(strings.TrimPrefix(base, "ptp"), &idx)
			return idx, err
		}
	}

	return -1, fmt.Errorf("no PTP clock index found for %s", iface)
}

// GetEthtoolInfo uses ethtool -i to get driver and firmware information
// This is more reliable than sysfs paths which vary by device type
func GetEthtoolInfo(deviceName string) (*EthtoolInfo, error) {
	if !ethtoolInstalled() {
		return nil, fmt.Errorf("ethtool not installed")
	}

	cmd := exec.Command("ethtool", "-i", deviceName)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ethtool -i %s failed: %v", deviceName, err)
	}

	info := &EthtoolInfo{}
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		switch key {
		case "driver":
			info.Driver = value
		case "version":
			info.DriverVersion = value
		case "firmware-version":
			info.FirmwareVersion = value
		case "bus-info":
			info.BusInfo = value
		case "expansion-rom-version":
			info.ExpansionRom = value
		}
	}
	return info, nil
}

// GetLinkInfo uses ethtool to get link status, speed, and FEC information
func GetLinkInfo(deviceName string) (*LinkInfo, error) {
	if !ethtoolInstalled() {
		return nil, fmt.Errorf("ethtool not installed")
	}

	info := &LinkInfo{}

	// Get link status and speed using: ethtool <deviceName>
	cmd := exec.Command("ethtool", deviceName)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ethtool %s failed: %v", deviceName, err)
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "Speed:") {
			speed := strings.TrimSpace(strings.TrimPrefix(line, "Speed:"))
			if speed != "Unknown!" {
				info.Speed = speed
			}
		} else if strings.HasPrefix(line, "Duplex:") {
			info.Duplex = strings.TrimSpace(strings.TrimPrefix(line, "Duplex:"))
		} else if strings.HasPrefix(line, "Link detected:") {
			value := strings.TrimSpace(strings.TrimPrefix(line, "Link detected:"))
			info.LinkDetected = (value == "yes")
		}
	}

	// Get FEC using: ethtool --show-fec <deviceName>
	fecCmd := exec.Command("ethtool", "--show-fec", deviceName)
	fecOutput, err := fecCmd.Output()
	if err != nil {
		// FEC query may not be supported on all devices - not an error
		glog.V(4).Infof("ethtool --show-fec %s not supported: %v", deviceName, err)
	} else {
		fecLines := strings.Split(string(fecOutput), "\n")
		for _, line := range fecLines {
			line = strings.TrimSpace(line)
			// Look for "Active FEC encoding:" line
			if strings.HasPrefix(line, "Active FEC encoding:") {
				info.FEC = strings.TrimSpace(strings.TrimPrefix(line, "Active FEC encoding:"))
				break
			}
			// Some drivers use "Configured FEC encodings:" when no active link
			if strings.HasPrefix(line, "Configured FEC encodings:") && info.FEC == "" {
				info.FEC = strings.TrimSpace(strings.TrimPrefix(line, "Configured FEC encodings:"))
			}
		}
	}

	return info, nil
}

// GetPCIAddress returns the PCI address for a network device
func GetPCIAddress(deviceName string) (string, error) {
	net, err := ghw.Network()
	if err != nil {
		return "", fmt.Errorf("failed to get network info: %v", err)
	}

	for _, nic := range net.NICs {
		if nic.Name == deviceName {
			if nic.PCIAddress != nil {
				return *nic.PCIAddress, nil
			}
			return "", fmt.Errorf("device %s has no PCI address", deviceName)
		}
	}
	return "", fmt.Errorf("device %s not found", deviceName)
}

// GetNetDeviceFromPCI returns the network device name for a given PCI address.
// It tries multiple sysfs paths since availability varies by environment (containers, etc.)
func GetNetDeviceFromPCI(pciAddress string) (string, error) {
	// Method 1: Try /sys/bus/pci/devices/<pci_addr>/net/
	netDir := fmt.Sprintf("/sys/bus/pci/devices/%s/net", pciAddress)
	entries, err := os.ReadDir(netDir)
	if err == nil && len(entries) > 0 {
		return entries[0].Name(), nil
	}

	// Method 2: Scan /sys/class/net/*/device symlinks
	netDevices, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return "", fmt.Errorf("cannot read /sys/class/net: %v", err)
	}

	for _, dev := range netDevices {
		deviceLink := filepath.Join("/sys/class/net", dev.Name(), "device")
		target, linkErr := os.Readlink(deviceLink)
		if linkErr != nil {
			continue
		}
		// The symlink target contains the PCI address as the last component
		if strings.HasSuffix(target, pciAddress) || strings.Contains(target, pciAddress) {
			return dev.Name(), nil
		}
	}

	return "", fmt.Errorf("no network device found for PCI address %s", pciAddress)
}

// ReadSysfsFile reads a single-line value from sysfs and trims whitespace
// Returns empty string silently if file doesn't exist (normal for many device types)
func ReadSysfsFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// GetVPDInfo reads and parses VPD (Vital Product Data) for a network device
// VPD contains manufacturing info like part number, serial number, etc.
func GetVPDInfo(deviceName string) (*VPDInfo, error) {
	vpdPath := fmt.Sprintf("/sys/class/net/%s/device/vpd", deviceName)
	data, err := os.ReadFile(vpdPath)
	if err != nil {
		return nil, fmt.Errorf("could not read VPD for %s: %v", deviceName, err)
	}

	return ParseVPD(data), nil
}

// GetVPDInfoByPCIPath reads and parses VPD from a PCI device path
func GetVPDInfoByPCIPath(pciPath string) (*VPDInfo, error) {
	vpdPath := filepath.Join(pciPath, "vpd")
	data, err := os.ReadFile(vpdPath)
	if err != nil {
		return nil, fmt.Errorf("could not read VPD: %v", err)
	}

	return ParseVPD(data), nil
}

// ParseVPD parses binary VPD data according to PCI Local Bus Specification
func ParseVPD(vpdFile []byte) *VPDInfo {
	vpd := &VPDInfo{}
	lenFile := len(vpdFile)
	if lenFile < pciVPDBlockDescriptorLen {
		return vpd
	}

	offset := 0
parseLoop:
	for offset < lenFile-pciVPDBlockDescriptorLen {
		tag := vpdFile[offset]
		blockDesc := vpdFile[offset : offset+pciVPDBlockDescriptorLen]
		l := blockDesc[1:pciVPDBlockDescriptorLen]
		lenBlock := int(binary.LittleEndian.Uint16(l))

		// Bounds check
		if offset+pciVPDBlockDescriptorLen+lenBlock > lenFile {
			break
		}

		block := vpdFile[offset+pciVPDBlockDescriptorLen : offset+pciVPDBlockDescriptorLen+lenBlock]
		offset += lenBlock + pciVPDBlockDescriptorLen

		switch tag {
		case pciVPDIDStringTag:
			vpd.IdentifierString = cleanVPDString(string(block))
		case pciVPDROTag:
			ro := parseVPDBlock(block)
			for k, v := range ro {
				switch k {
				case "SN":
					vpd.SerialNumber = v
				case "PN":
					vpd.PartNumber = v
				case "MN":
					vpd.ManufacturerID = v
				case "V0":
					vpd.ProductName = v
				case "V1":
					vpd.VendorSpecific1 = v
				case "V2":
					vpd.VendorSpecific2 = v
				}
			}
		case pciVPDEndTag:
			break parseLoop
		}
	}

	return vpd
}

// parseVPDBlock parses a VPD read-only or read-write block
func parseVPDBlock(block []byte) map[string]string {
	rv := map[string]string{}
	lenBlock := len(block)
	offset := 0

	for offset+pciVPDKeywordLen+1 <= lenBlock {
		kw := string(block[offset : offset+pciVPDKeywordLen])
		ln := int(block[offset+pciVPDKeywordLen])

		dataStart := offset + pciVPDKeywordLen + 1
		dataEnd := dataStart + ln

		if dataEnd > lenBlock {
			break
		}

		data := block[dataStart:dataEnd]
		// Extract common fields
		if strings.HasPrefix(kw, "V") || kw == "PN" || kw == "SN" || kw == "MN" {
			rv[kw] = cleanVPDString(string(data))
		}

		offset = dataEnd
	}

	return rv
}

// cleanVPDString removes null bytes and non-printable characters from VPD strings
func cleanVPDString(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 32 && r < 127 {
			return r
		}
		return -1
	}, strings.TrimSpace(s))
}
