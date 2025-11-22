package intel

import (
	"fmt"

	"github.com/golang/glog"
)

// DevicePinConfig is a map of interface -> Pin -> pin state
type DevicePinConfig map[string]map[string]string

func applyDevicePins(devicePins DevicePinConfig) {
	for device, pins := range devicePins {
		for pin, value := range pins {
			deviceDir := fmt.Sprintf("/sys/class/net/%s/device/ptp/", device)
			phcs, err := filesystem.ReadDir(deviceDir)
			if err != nil {
				glog.Error("failed to read " + deviceDir + ": " + err.Error())
				continue
			}
			for _, phc := range phcs {
				pinPath := fmt.Sprintf("/sys/class/net/%s/device/ptp/%s/pins/%s", device, phc.Name(), pin)
				glog.Infof("echo %s > %s", value, pinPath)
				err = filesystem.WriteFile(pinPath, []byte(value), 0o666)
				if err != nil {
					glog.Error("failed to write " + value + " to " + pinPath + ": " + err.Error())
				}
			}
		}
	}
}
