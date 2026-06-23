package config_test

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/stretchr/testify/assert"
)

const testIface = "eno8703"

func TestGetLeadingInterface(t *testing.T) {
	tests := []struct {
		name      string
		ifaces    config.IFaces
		wantName  string
		wantEmpty bool
	}{
		{
			name: "returns GNSS iface",
			ifaces: config.IFaces{
				{Name: testIface, Source: event.GNSS},
			},
			wantName: testIface,
		},
		{
			name: "returns PTP4l iface (BC case)",
			ifaces: config.IFaces{
				{Name: "ens2f0", Source: event.PTP4l},
			},
			wantName: "ens2f0",
		},
		{
			// GM ts2phc slave ifaces have Source=PPS because their ts2phc.master
			// option is 0; the [nmea] master section is excluded from the ifaces
			// list by RenderPtp4lConf. Without PPS support here, GetLeadingInterface
			// returns an empty Iface, causing gmInterface="" in GPSD and
			// persistent "Sanity check failed" warnings for gnss and GM metrics.
			name: "returns PPS iface (GM ts2phc slave case)",
			ifaces: config.IFaces{
				{Name: testIface, Source: event.PPS},
				{Name: "enp108s0f0", Source: event.PPS},
			},
			wantName: testIface,
		},
		{
			// First match wins; both PPS and GNSS qualify.
			name: "returns first matching iface when multiple sources qualify",
			ifaces: config.IFaces{
				{Name: testIface, Source: event.PPS},
				{Name: "enp108s0f0", Source: event.GNSS},
			},
			wantName: testIface,
		},
		{
			name:      "returns empty Iface when no matching source",
			ifaces:    config.IFaces{{Name: testIface, Source: event.MONITORING}},
			wantEmpty: true,
		},
		{
			name:      "returns empty Iface for empty list",
			ifaces:    config.IFaces{},
			wantEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.ifaces.GetLeadingInterface()
			if tt.wantEmpty {
				assert.Empty(t, got.Name)
			} else {
				assert.Equal(t, tt.wantName, got.Name)
			}
		})
	}
}
