package dpll_netlink

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func prio(v uint32) *uint32 { return &v }

func TestHasRelevantParent(t *testing.T) {
	tests := []struct {
		name     string
		pin      *PinInfo
		expected bool
	}{
		{
			name: "selectable parent is relevant",
			pin: &PinInfo{ParentDevice: []PinParentDevice{
				{State: PinStateSelectable},
			}},
			expected: true,
		},
		{
			name: "connected parent is relevant",
			pin: &PinInfo{ParentDevice: []PinParentDevice{
				{State: PinStateConnected},
			}},
			expected: true,
		},
		{
			name: "active operstate is relevant",
			pin: &PinInfo{ParentDevice: []PinParentDevice{
				{State: PinStateDisconnected, Operstate: PinOperstateActive},
			}},
			expected: true,
		},
		{
			name: "disconnected only is not relevant",
			pin: &PinInfo{ParentDevice: []PinParentDevice{
				{State: PinStateDisconnected},
			}},
			expected: false,
		},
		{
			name:     "no parents is not relevant",
			pin:      &PinInfo{},
			expected: false,
		},
		{
			name: "mixed parents, one relevant",
			pin: &PinInfo{ParentDevice: []PinParentDevice{
				{State: PinStateDisconnected},
				{State: PinStateSelectable},
			}},
			expected: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, hasRelevantParent(tt.pin))
		})
	}
}

func TestGroupPinsByClockID(t *testing.T) {
	pins := []*PinInfo{
		{
			ID: 17, ClockID: 0xAABB, BoardLabel: "CVL-SDP22",
			ParentDevice: []PinParentDevice{
				{ParentID: 2, Direction: PinDirectionInput, State: PinStateSelectable, Prio: prio(255)},
			},
		},
		{
			ID: 29, ClockID: 0xAABB, BoardLabel: "CVL-SDP23",
			ParentDevice: []PinParentDevice{
				{ParentID: 3, Direction: PinDirectionOutput, State: PinStateConnected},
			},
		},
		{
			ID: 0, ClockID: 0xCCDD, BoardLabel: "CVL-SDP22",
			ParentDevice: []PinParentDevice{
				{ParentID: 0, Direction: PinDirectionInput, State: PinStateSelectable, Prio: prio(5)},
			},
		},
		{
			ID: 99, ClockID: 0xEEFF, BoardLabel: "HIDDEN",
			ParentDevice: []PinParentDevice{
				{State: PinStateDisconnected},
			},
		},
	}

	groups := groupPinsByClockID(pins)

	assert.Equal(t, 2, len(groups), "should have 2 clock groups (0xEEFF filtered out)")
	assert.Equal(t, uint64(0xAABB), groups[0].clockID)
	assert.Equal(t, 2, len(groups[0].lines), "0xAABB has 2 pins")
	assert.Equal(t, uint64(0xCCDD), groups[1].clockID)
	assert.Equal(t, 1, len(groups[1].lines), "0xCCDD has 1 pin")

	assert.Contains(t, groups[0].lines[0], "CVL-SDP22")
	assert.Contains(t, groups[0].lines[0], "prio=255")
	assert.Contains(t, groups[0].lines[1], "CVL-SDP23")
	assert.Contains(t, groups[0].lines[1], "admin=connected")
	assert.Contains(t, groups[1].lines[0], "prio=5")
}

func TestGroupPinsByClockID_Empty(t *testing.T) {
	groups := groupPinsByClockID(nil)
	assert.Empty(t, groups)
}

func TestGroupPinsByClockID_AllFiltered(t *testing.T) {
	pins := []*PinInfo{
		{
			ID: 1, ClockID: 0xAA, BoardLabel: "X",
			ParentDevice: []PinParentDevice{{State: PinStateDisconnected}},
		},
	}
	groups := groupPinsByClockID(pins)
	assert.Empty(t, groups)
}

func TestGroupPinsByClockID_PrioNil(t *testing.T) {
	pins := []*PinInfo{
		{
			ID: 5, ClockID: 0xBB, BoardLabel: "OUT",
			ParentDevice: []PinParentDevice{
				{ParentID: 0, Direction: PinDirectionOutput, State: PinStateConnected, Prio: nil},
			},
		},
	}
	groups := groupPinsByClockID(pins)
	assert.Equal(t, 1, len(groups))
	assert.Contains(t, groups[0].lines[0], "prio=n/a")
}
