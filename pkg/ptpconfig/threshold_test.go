package ptpconfig

import (
	"testing"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func TestResolvePTPThreshold(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile ptpv1.PtpProfile
		max     int64
		min     int64
		hold    int64
	}{
		{name: "default", max: 100, min: -100, hold: 5},
		{name: "configured", profile: ptpv1.PtpProfile{PtpClockThreshold: &ptpv1.PtpClockThreshold{MaxOffsetThreshold: 500, MinOffsetThreshold: -300, HoldOverTimeout: 10}}, max: 500, min: -300, hold: 10},
		{name: "NTP failover", profile: ptpv1.PtpProfile{Plugins: map[string]*apiextensions.JSON{"ntpfailover": {Raw: []byte(`{"gnssFailover":true}`)}}}, max: 1000, min: -1000, hold: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolvePTPThreshold(&tc.profile)
			if got.MaxOffsetThreshold != tc.max || got.MinOffsetThreshold != tc.min || got.HoldOverTimeout != tc.hold {
				t.Fatalf("threshold = %+v, want max=%d min=%d hold=%d", got, tc.max, tc.min, tc.hold)
			}
		})
	}
}
