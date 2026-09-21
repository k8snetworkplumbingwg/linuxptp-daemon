# HardwareConfig v2 device selection

This document records how GNSS device detection currently works and evaluates where
HardwareConfig v2 could use stable Ethernet-device selectors instead of interface
names.

## GNSS matcher

`GNSSConfig.Match` currently supports exactly one of:

- `ttyDevice`
- `serialDevice`
- `ethernetDevice`
- `usbDevice`

The selector should resolve to exactly one usable GNSS device. A zero-match or
multiple-match result is an error; the implementation must not choose the first
lexicographically sorted device when the selector is ambiguous.

### Direct tty selection

When `ttyDevice` is specified, it is returned as provided. This is the most
explicit option, but paths such as `/dev/ttyACM0` can change after reboot or
hotplug events.

### Ethernet-device selection

`ethernetDevice` is an Ethernet-device selector inspired by the SR-IOV Network
Operator's `nicSelector`. It currently supports:

```yaml
ethernetDevice:
  name: eno8703
```

or PCI-oriented criteria:

```yaml
ethernetDevice:
  pciSlot: "0000:86:00.0"
  vendor: "8086"
  deviceID: "159b"
```

Selection behavior:

1. If `name` is provided, it takes the direct lookup path:
   `/sys/class/net/<name>/device/gnss/`.
2. If `name` is omitted and `pciSlot` is provided, interfaces are found under:
   `/sys/bus/pci/devices/<pci-slot>/net/`.
3. If `vendor` and/or `deviceID` are provided, interfaces are enumerated from
   `/sys/class/net`. Their resolved PCI device directories are matched against
   the PCI `vendor` and `device` attributes.
4. When multiple non-name criteria are provided, they are combined as AND
   criteria.
5. The selected interface's `device/gnss/` sysfs directory is used to resolve
   the GNSS device node.

A short PCI address such as `86:00.0` may be normalized to the full domain form
`0000:86:00.0`. PCI vendor and device IDs are hexadecimal values and are
normalized before comparison.

The name form is intentionally a fast, direct lookup. If a name is supplied
alongside other criteria, the current behavior is to use the name rather than
perform an additional identity check. A future API revision could instead
validate all supplied fields together if that proves useful.

### ACPI serial-device selection

`serialDevice` identifies an onboard serial controller by platform hardware
identity instead of by the dynamically assigned Linux tty name. The selector
currently supports ACPI identity:

```yaml
serialDevice:
  acpi:
    hid: INTC10EE
```

The detector enumerates `/sys/class/tty/*`, resolves each tty's `device`
symlink, and walks its parent directories looking for an ACPI device directory
named `<hid>:<uid>`. If `uid` is omitted, any instance of the HID may match.
Here `uid` means the Linux ACPI device instance suffix in the sysfs directory
name; it is not the value of the ACPI `_UID` attribute. The result is
`/dev/<tty-name>`, and zero or multiple matching ttys are errors.

For the HPE EL140, HPE documents the GNSS-connected controller as:

```text
ACPI device: INTC10EE:00
Linux driver: dw-apb-uart
Typical tty: /dev/ttyS2
```

The Linux name `ttyS2` is not used as the selector because serial numbering can
change when other UARTs are enumerated. The ACPI HID and UID identify the
controller; the HPE EL140 hardware profile supplies the platform-specific
knowledge that this controller is wired to the GNSS module. The driver name is
not required for matching because it is an implementation detail of the Linux
kernel.

The HPE EL140 profile therefore uses:

```yaml
match:
  serialDevice:
    acpi:
      hid: INTC10EE
```

This is a hardware identity match, not a generic proof that every system with
`INTC10EE:00` has a GNSS receiver attached.

#### Live EL140 verification

The selector was checked on a live EL140 node. Its tty and platform-device
relationships were:

```text
ttyS0 -> /sys/devices/pnp0/00:03
ttyS1 -> /sys/devices/pnp0/00:02
ttyS2 -> /sys/devices/platform/INTC10EE:00
ttyS3 -> /sys/devices/platform/serial8250
```

The matching device reported:

```text
/sys/devices/platform/INTC10EE:00/driver -> /sys/bus/platform/drivers/dw-apb-uart
/sys/devices/platform/INTC10EE:00/modalias = acpi:INTC10EE:
/dev/ttyS2 exists
/dev/ttyACM0 is absent
```

The ACPI sysfs entry also reported `uid = 1` at
`/sys/bus/acpi/devices/INTC10EE:00/uid`. This is distinct from the `:00`
Linux device-instance suffix in the platform-device name. The matcher uses the
platform-device ancestry and therefore deliberately matches `hid: INTC10EE`
without requiring either UID value. It does not need to inspect the UART driver
name; `dw-apb-uart` is recorded as a diagnostic confirmation rather than a
selection criterion.

The live result confirms that the selector resolves `/dev/ttyS2` without relying
on the dynamically assigned tty number. The serial stream itself was not read
during verification.

### USB-device selection

The GNR-D GNSS receiver is selected using:

```yaml
usbDevice:
  vendor: "1546"
  product: "01a9"
```

The detector does not depend on the tty name or on the `ttyACM`/`ttyUSB` driver
name. It performs the following sysfs traversal:

1. Enumerate `/sys/class/tty/*`.
2. Ignore entries without a resolvable `device` symlink. This excludes virtual
   tty devices and stale class entries.
3. Resolve `/sys/class/tty/<tty>/device`.
4. Walk the resolved device's parent directories.
5. Identify USB device ancestors by their `subsystem` symlink and read
   `idVendor` and `idProduct`.
6. Compare the normalized hexadecimal IDs with the requested selector.
7. Return `/dev/<tty-name>` if exactly one tty matches.

On the GNR-D system the relevant topology is:

```text
/sys/class/tty/ttyACM0/device
  -> /sys/devices/.../usb1/1-4/1-4:1.0
  -> /sys/devices/.../usb1/1-4
```

The USB attributes are located at the `1-4` device node:

```text
/sys/devices/.../usb1/1-4/idVendor      = 1546
/sys/devices/.../usb1/1-4/idProduct     = 01a9
/sys/devices/.../usb1/1-4/manufacturer  = u-blox AG - www.u-blox.com
/sys/devices/.../usb1/1-4/product       = u-blox GNSS receiver
```

The result is `/dev/ttyACM0`. The device has multiple USB interfaces, but only
one currently produces a tty. If a future device exposes multiple tty nodes,
the VID/PID selector alone is insufficient and detection must fail with an
ambiguity error or use an additional interface-level criterion.

## SR-IOV comparison

The SR-IOV Network Operator's `SriovNetworkNodePolicy.spec.nicSelector` supports
these related criteria:

- `vendor`
- `deviceID`
- `pfNames`
- `rootDevices` (PCI addresses)
- `netFilter` for platform-specific network identity

Its documented guidance is to provide enough criteria to avoid unintended
matches. `rootDevices` must be combined with another identifying criterion,
and `pfNames` plus `rootDevices` must refer to the same device. A `netFilter`
can be used alone where the platform network identifier is unique.

HardwareConfig v2 currently uses a simpler model:

| SR-IOV concept | HardwareConfig v2 equivalent | Notes |
|---|---|---|
| `pfNames` | `ethernetDevice.name` | Direct Linux interface lookup |
| `rootDevices` | `ethernetDevice.pciSlot` | PCI function/BDF lookup |
| `vendor` | `ethernetDevice.vendor` | PCI vendor ID |
| `deviceID` | `ethernetDevice.deviceID` | PCI device ID |
| `netFilter` | None | Platform-specific network identity is not currently needed |

The relevant precedence is not “pick one matching field”; it is:

- use an explicitly named interface directly;
- otherwise combine the supplied hardware identity fields;
- require the resulting GNSS device to be unique.

## Other HardwareConfig v2 Ethernet references

There are two additional places where HardwareConfig v2 represents Ethernet
hardware using interface names.

### 1. `Subsystem.DPLL.NetworkInterface`

```yaml
dpll:
  networkInterface: eno8703
```

This is the strongest candidate for a richer selector. It identifies the
interface used to derive or look up the DPLL clock ID and is used throughout the
daemon for:

- clock ID resolution;
- phase-adjustment and delay-compensation lookup;
- applying hardware defaults;
- resolving subsystem-level operations;
- fallback selection when an Ethernet port is present.

The current resolution precedence is roughly:

1. Explicit `dpll.networkInterface`.
2. A leading interface derived from a PTP port by following its PHC to the PCI
   device and then enumerating `/sys/bus/pci/devices/<pci>/net/`.
3. The first Ethernet port in `subsystem.ethernet[].ports`.
4. For some clock types, the first interface found in the PTP profile.

This existing PHC-to-PCI-to-network traversal is a useful precedent for adding
stable matching. A future API could add a `dpll.networkDevice` selector while
retaining a resolved `networkInterface` string internally. This would avoid
changing every downstream consumer at once.

**Assessment: high value, but broad implementation impact.** This field is the
best next candidate after GNSS matching because a renamed interface can cause
clock-ID and hardware-resolution failures.

### 2. `Subsystem.Ethernet[].Ports`

```yaml
ethernet:
  - ports:
      - eno8703
      - eno8803
```

This is a list rather than a single device. The first port is significant: it is
currently treated as the default interface when `dpll.networkInterface` is
omitted. The list is also used to associate multiple physical Ethernet ports
with one synchronization subsystem and to validate PTP receiver membership.

A richer selector could be useful for systems where predictable interface names
are unavailable, but it requires defining list semantics first:

- Does one selector resolve to one port or all ports on a PCI function?
- Does a PCI device with multiple ports expand to multiple interfaces?
- How is the default/first port selected?
- Can a selector match a PF while the list is intended to contain individual
  ports?
- How should explicit ordering be preserved?

**Assessment: potentially high value, but not a drop-in replacement.** This
should likely use a list of selectors or a separate `EthernetDeviceGroup` type,
not simply replace each string with one `EthernetDevice` object.

### 3. `SourceConfig.PTPTimeReceivers`

```yaml
sources:
  - sourceType: ptpTimeReceiver
    ptpTimeReceivers:
      - eno8703
```

These values identify ports used by the PTP behavior and are derived from the
PTP profile's `ptp4l` configuration in common cases. They are not purely
hardware selectors: they also correspond to runtime PTP interface sections and
must remain valid names for the generated/configured PTP process.

Replacing these strings with hardware selectors would require resolving the
selectors before generating the PTP configuration and carefully handling
multiple matches. It could be useful for a higher-level declarative API, but it
would blur the distinction between:

- identifying hardware; and
- naming a port in a PTP runtime configuration.

**Assessment: lower priority.** Keep these as resolved interface names for now.
If selector support is added later, resolve selectors during HardwareConfig
resolution and keep the runtime representation as interface names.

## Recommended direction

1. Keep the current `EthernetDevice` selector for GNSS.
2. Introduce a reusable internal Ethernet selector/resolver abstraction without
   immediately changing every API field.
3. Add a selector alongside `DPLL.NetworkInterface`, resolve it once to a
   concrete interface name, and keep downstream code name-based.
4. Address `Ethernet[].Ports` separately because it represents an ordered,
   potentially multi-port group.
5. Leave `PTPTimeReceivers` as concrete runtime interface names until selector
   expansion is integrated with PTP configuration generation.
6. For every selector, use AND semantics for supplied identity fields and fail
   on zero or multiple matches.

## References

- SR-IOV Network Operator node policy API: `SriovNetworkNodePolicy.spec.nicSelector`
  ([API reference](https://github.com/k8snetworkplumbingwg/sriov-network-operator/blob/master/doc/api/node-policies-api.md))
- OpenShift SR-IOV device selection documentation:
  [Configuring an SR-IOV network device](https://docs.redhat.com/en/documentation/openshift_container_platform/4.21/html/hardware_networks/configuring-sriov-device)
- HPE EL140 UART advisory:
  [a00157521en_us](https://support.hpe.com/hpesc/public/docDisplay?docId=a00157521en_us&docLocale=en_US)
- HardwareConfig v2 API: `../ptp-operator/api/v2alpha1/hardwareconfig_types.go`
