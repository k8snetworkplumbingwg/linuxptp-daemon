# Feature Flags

This package provides a central point of truth for the codebase, indicating which features are available for execution.
It determines feature availability based on the installed version of the linuxptp package.
Integrating this package allows for conditional code execution depending on the enabled features.

## Example

```go
import (
    "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/features"
)

func New(
    ...
) *Daemon {
    ...
    features.Flags.Init(getLinuxPTPPackageVersion())
    features.Flags.Print()
    ...
}

func (dn *Daemon) applyNodePtpProfile(runID int, nodeProfile *ptpv1.PtpProfile) error {
    ...
    if section.sectionName == "[global]" {
        section.options["message_tag"] = messageTag
        if socketPath != "" {
            section.options["uds_address"] = socketPath
        }

        if feature.Flags.IsGMAvailable() { // Only runs if Grand master is enabled
            if gnssSerialPort, ok := section.options["ts2phc.nmea_serialport"]; ok {
                output.gnss_serial_port = strings.TrimSpace(gnssSerialPort)
                section.options["ts2phc.nmea_serialport"] = GPSPIPE_SERIALPORT
            }
            if _, ok := section.options["leapfile"]; ok || pProcess == ts2phcProcessName { // not required to check process if leapfile is always included
                ection.options["leapfile"] = fmt.Sprintf("%s/%s", config.DefaultLeapConfigPath, os.Getenv("NODE_NAME"))
            }
        }
        ...
        output.sections[index] = section
    }
    ...
}
```
