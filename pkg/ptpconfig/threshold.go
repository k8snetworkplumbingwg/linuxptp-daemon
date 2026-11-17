package ptpconfig

import (
	"encoding/json"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

// ResolvePTPThreshold applies the clock threshold defaults used by BC and OC clocks.
func ResolvePTPThreshold(profile *ptpv1.PtpProfile) event.PtpClockThreshold {
	maxOffset, minOffset, holdoverTimeout := int64(100), int64(-100), int64(5)
	if th := profile.PtpClockThreshold; th != nil {
		if th.MaxOffsetThreshold > 0 {
			maxOffset = th.MaxOffsetThreshold
		}
		if th.MinOffsetThreshold > maxOffset {
			minOffset = maxOffset - 1
		} else {
			minOffset = th.MinOffsetThreshold
		}
		if th.HoldOverTimeout > 0 {
			holdoverTimeout = th.HoldOverTimeout
		}
	} else if plugin := profile.Plugins["ntpfailover"]; plugin != nil {
		var opts struct {
			GnssFailover bool `json:"gnssFailover"`
		}
		if json.Unmarshal(plugin.Raw, &opts) == nil && opts.GnssFailover {
			maxOffset, minOffset = 1000, -1000
		}
	}
	return event.PtpClockThreshold{
		MaxOffsetThreshold: maxOffset,
		MinOffsetThreshold: minOffset,
		HoldOverTimeout:    holdoverTimeout,
	}
}
