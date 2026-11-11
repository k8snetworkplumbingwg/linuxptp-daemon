package clock

import (
	"math"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/stretchr/testify/assert"
)

func testTstamp(device string, timestamp float64, role event.TS2PHCRole) tstamp {
	seconds := int64(math.Floor(timestamp))
	nanoseconds := int64(math.Round((timestamp - float64(seconds)) * 1e9))
	if nanoseconds == 1_000_000_000 {
		seconds++
		nanoseconds = 0
	}
	return tstamp{device: device, seconds: seconds, nanoseconds: nanoseconds, role: role}
}

func TestComputeTSOffset(t *testing.T) {
	stamp := func(device string, timestamp float64) *tstamp {
		stamp := testTstamp(device, timestamp, "")
		return &stamp
	}
	peers := func(timestamps ...tstamp) *pollCycle {
		return &pollCycle{tstamps: timestamps}
	}
	source := func(device string, timestamp float64) *pollCycle {
		return peers(testTstamp(device, timestamp, event.TS2PHCSource))
	}
	tests := []struct {
		name  string
		ts    *tstamp
		cycle *pollCycle
		want  float64
	}{
		{
			name: "nil timestamp and cycle",
			want: math.NaN(),
		},
		{
			name: "nil cycle",
			ts:   stamp("/dev/ptp0", 10),
			want: math.NaN(),
		},
		{
			name:  "nil timestamp with empty cycle",
			cycle: &pollCycle{},
			want:  math.NaN(),
		},
		{
			name:  "nil timestamp with source",
			cycle: source("/dev/ptp1", 11),
			want:  math.NaN(),
		},
		{
			name:  "no source or peers",
			ts:    stamp("/dev/ptp0", 10),
			cycle: &pollCycle{},
			want:  math.NaN(),
		},
		{
			name:  "only self is present",
			ts:    stamp("/dev/ptp0", 10),
			cycle: peers(tstamp{device: "/dev/ptp0", seconds: 10, nanoseconds: 0}),
			want:  math.NaN(),
		},
		{
			name: "same device entries are excluded even with different timestamps",
			ts:   stamp("/dev/ptp0", 10),
			cycle: peers(
				tstamp{device: "/dev/ptp0", seconds: 9, nanoseconds: 0},
				tstamp{device: "/dev/ptp0", seconds: 11, nanoseconds: 0},
			),
			want: math.NaN(),
		},
		{
			name:  "explicit source ahead",
			ts:    stamp("/dev/ptp0", 10),
			cycle: source("/dev/ptp1", 11),
			want:  1_000_000_000,
		},
		{
			name:  "explicit source behind preserves sign",
			ts:    stamp("/dev/ptp0", 11),
			cycle: source("/dev/ptp1", 10),
			want:  -1_000_000_000,
		},
		{
			name:  "explicit source equal",
			ts:    stamp("/dev/ptp0", 10),
			cycle: source("/dev/ptp1", 10),
			want:  0,
		},
		{
			name:  "explicit source fractional difference across second boundary",
			ts:    stamp("/dev/ptp0", 10.75),
			cycle: source("/dev/ptp1", 11.25),
			want:  500_000_000,
		},
		{
			name:  "explicit source at realistic epoch",
			ts:    stamp("/dev/ptp0", 1791217457.25),
			cycle: source("/dev/ptp1", 1791217457.5),
			want:  250_000_000,
		},
		{
			name:  "zero timestamp is a valid measurement",
			ts:    stamp("/dev/ptp0", 0),
			cycle: source("/dev/ptp1", 0),
			want:  0,
		},
		{
			name:  "same-device source cannot measure an offset",
			ts:    stamp("/dev/ptp0", 10),
			cycle: source("/dev/ptp0", 11),
			want:  math.NaN(),
		},
		{
			name: "explicit source takes precedence over peers",
			ts:   stamp("/dev/ptp0", 10),
			cycle: peers(
				tstamp{device: "/dev/ptp1", seconds: 9, nanoseconds: 0, role: event.TS2PHCSource},
				tstamp{device: "/dev/ptp2", seconds: 20, nanoseconds: 0, role: event.TS2PHCSink},
			),
			want: -1_000_000_000,
		},
		{
			name: "furthest source preserves its negative sign",
			ts:   stamp("/dev/ptp0", 10),
			cycle: peers(
				tstamp{device: "/dev/ptp1", seconds: 11, nanoseconds: 0, role: event.TS2PHCSource},
				tstamp{device: "/dev/ptp2", seconds: 7, nanoseconds: 0, role: event.TS2PHCSource},
				tstamp{device: "/dev/ptp3", seconds: 100, nanoseconds: 0, role: event.TS2PHCUnmanaged},
			),
			want: -3_000_000_000,
		},
		{
			name: "furthest source preserves its positive sign",
			ts:   stamp("/dev/ptp0", 10),
			cycle: peers(
				tstamp{device: "/dev/ptp1", seconds: 9, nanoseconds: 0, role: event.TS2PHCSource},
				tstamp{device: "/dev/ptp2", seconds: 13, nanoseconds: 0, role: event.TS2PHCSource},
			),
			want: 3_000_000_000,
		},
		{
			name: "valid zero source offset does not fall back to peers",
			ts:   stamp("/dev/ptp0", 10),
			cycle: peers(
				tstamp{device: "/dev/ptp1", seconds: 10, nanoseconds: 0, role: event.TS2PHCSource},
				tstamp{device: "/dev/ptp2", seconds: 20, nanoseconds: 0, role: event.TS2PHCSink},
			),
			want: 0,
		},
		{
			name: "same-device source falls back to another peer",
			ts:   stamp("/dev/ptp0", 10),
			cycle: peers(
				tstamp{device: "/dev/ptp0", seconds: 100, nanoseconds: 0, role: event.TS2PHCSource},
				tstamp{device: "/dev/ptp1", seconds: 9, nanoseconds: 0, role: event.TS2PHCSink},
			),
			want: 1_000_000_000,
		},
		{
			name:  "peer ahead returns nanoseconds",
			ts:    stamp("/dev/ptp0", 10),
			cycle: peers(tstamp{device: "/dev/ptp1", seconds: 11, nanoseconds: 0}),
			want:  1_000_000_000,
		},
		{
			name:  "peer behind returns absolute delta",
			ts:    stamp("/dev/ptp0", 10),
			cycle: peers(tstamp{device: "/dev/ptp1", seconds: 9, nanoseconds: 0}),
			want:  1_000_000_000,
		},
		{
			name:  "equal peer is measured zero rather than unavailable",
			ts:    stamp("/dev/ptp0", 10),
			cycle: peers(tstamp{device: "/dev/ptp1", seconds: 10, nanoseconds: 0}),
			want:  0,
		},
		{
			name:  "fractional peer difference across second boundary",
			ts:    stamp("/dev/ptp0", 10.75),
			cycle: peers(tstamp{device: "/dev/ptp1", seconds: 11, nanoseconds: 250000000}),
			want:  500_000_000,
		},
		{
			name: "largest positive peer delta wins regardless of order",
			ts:   stamp("/dev/ptp0", 10),
			cycle: peers(
				tstamp{device: "/dev/ptp1", seconds: 11, nanoseconds: 0},
				tstamp{device: "/dev/ptp2", seconds: 13, nanoseconds: 0},
				tstamp{device: "/dev/ptp3", seconds: 12, nanoseconds: 0},
			),
			want: 3_000_000_000,
		},
		{
			name: "negative delta with larger magnitude wins",
			ts:   stamp("/dev/ptp0", 10),
			cycle: peers(
				tstamp{device: "/dev/ptp1", seconds: 1, nanoseconds: 0},
				tstamp{device: "/dev/ptp2", seconds: 11, nanoseconds: 0},
			),
			want: 9_000_000_000,
		},
		{
			name: "all peers behind return largest absolute delta",
			ts:   stamp("/dev/ptp0", 10),
			cycle: peers(
				tstamp{device: "/dev/ptp1", seconds: 8, nanoseconds: 0},
				tstamp{device: "/dev/ptp2", seconds: 9, nanoseconds: 0},
			),
			want: 2_000_000_000,
		},
		{
			name: "self cannot dominate peer comparison",
			ts:   stamp("/dev/ptp0", 10),
			cycle: peers(
				tstamp{device: "/dev/ptp0", seconds: 100, nanoseconds: 0},
				tstamp{device: "/dev/ptp1", seconds: 11, nanoseconds: 0},
			),
			want: 1_000_000_000,
		},
		{
			name: "peer eligibility depends on device not interface or handled status",
			ts:   &tstamp{device: "/dev/ptp0", iface: "ens0", seconds: 10, nanoseconds: 0, handled: true},
			cycle: peers(
				tstamp{device: "/dev/ptp1", iface: "ens0", seconds: 11, nanoseconds: 0, handled: true},
			),
			want: 1_000_000_000,
		},
		{
			name:  "10ns offset",
			ts:    &tstamp{device: "/dev/ptp0", iface: "ens0", seconds: 1792458374, nanoseconds: 999999999, handled: true},
			cycle: peers(tstamp{device: "/dev/ptp1", seconds: 1792458374, nanoseconds: 999999989}),
			want:  10,
		},
		{
			name:  "1ns offset",
			ts:    &tstamp{device: "/dev/ptp0", seconds: 1792458374, nanoseconds: 999999999},
			cycle: peers(tstamp{device: "/dev/ptp1", seconds: 1792458374, nanoseconds: 999999998}),
			want:  1,
		},
		{
			name:  "negative 10ns source offset",
			ts:    &tstamp{device: "/dev/ptp0", seconds: 1792458374, nanoseconds: 999999999},
			cycle: peers(tstamp{device: "/dev/ptp1", seconds: 1792458374, nanoseconds: 999999989, role: event.TS2PHCSource}),
			want:  -10,
		},
		{
			name:  "negative 1ns source offset",
			ts:    &tstamp{device: "/dev/ptp0", seconds: 1792458374, nanoseconds: 999999999},
			cycle: peers(tstamp{device: "/dev/ptp1", seconds: 1792458374, nanoseconds: 999999998, role: event.TS2PHCSource}),
			want:  -1,
		},
		{
			name:  "positive 100-year source offset",
			ts:    &tstamp{device: "/dev/ptp0", seconds: 1_000_000_000},
			cycle: peers(tstamp{device: "/dev/ptp1", seconds: 4_155_760_000, role: event.TS2PHCSource}),
			want:  3_155_760_000_000_000_000,
		},
		{
			name:  "negative 100-year source offset",
			ts:    &tstamp{device: "/dev/ptp0", seconds: 4_155_760_000},
			cycle: peers(tstamp{device: "/dev/ptp1", seconds: 1_000_000_000, role: event.TS2PHCSource}),
			want:  -3_155_760_000_000_000_000,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeTSOffset(tt.ts, tt.cycle)
			if math.IsNaN(tt.want) {
				assert.True(t, math.IsNaN(got), "unmeasured offset must be NaN, got %v", got)
				return
			}
			assert.Equal(t, tt.want, got)
		})
	}
}
