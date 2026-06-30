package dpll_netlink

import (
	"fmt"
	"strings"

	"github.com/golang/glog"
)

// LogPinTable fetches all DPLL pins and logs a filtered table grouped by clock ID.
// Only pins with at least one parent in "selectable", "connected", or operstate "active"
// are included. Each clock ID gets its own table section.
// Hardware-independent: no hardcoded labels, no clock ID filtering.
func LogPinTable(reason string) {
	conn, err := Dial(nil)
	if err != nil {
		glog.Errorf("pin table: failed to dial DPLL: %v", err)
		return
	}
	defer conn.Close() //nolint:errcheck

	pins, err := conn.DumpPinGet()
	if err != nil {
		glog.Errorf("pin table: failed to dump pins: %v", err)
		return
	}

	grouped := groupPinsByClockID(pins)
	if len(grouped) == 0 {
		glog.Infof("=== DPLL pin table (%s): no relevant pins ===", reason)
		return
	}

	for _, g := range grouped {
		glog.Infof("=== DPLL pin table (%s) clockID=0x%x ===\n%s\n=== end pin table 0x%x ===",
			reason, g.clockID, strings.Join(g.lines, "\n"), g.clockID)
	}
}

type pinGroup struct {
	clockID uint64
	lines   []string
}

func groupPinsByClockID(pins []*PinInfo) []pinGroup {
	orderMap := map[uint64]int{}
	var groups []pinGroup

	for _, pin := range pins {
		if !hasRelevantParent(pin) {
			continue
		}
		idx, exists := orderMap[pin.ClockID]
		if !exists {
			idx = len(groups)
			orderMap[pin.ClockID] = idx
			groups = append(groups, pinGroup{clockID: pin.ClockID})
		}
		label := pin.BoardLabel
		if pin.PackageLabel != "" {
			label = fmt.Sprintf("%s/%s", pin.BoardLabel, pin.PackageLabel)
		}
		for _, pd := range pin.ParentDevice {
			prioStr := "n/a"
			if pd.Prio != nil {
				prioStr = fmt.Sprintf("%d", *pd.Prio)
			}
			groups[idx].lines = append(groups[idx].lines,
				fmt.Sprintf("  pin=%-3d %-20s parentID=%-2d dir=%-6s prio=%-4s admin=%-12s oper=%s",
					pin.ID, label, pd.ParentID,
					GetPinDirection(pd.Direction),
					prioStr,
					GetPinState(pd.State),
					GetPinOperstate(pd.Operstate)))
		}
	}
	return groups
}

// LogPinInfo logs each parent device of a pin in table format.
// Suitable for use after pin set commands to confirm the applied state.
func LogPinInfo(pin *PinInfo) {
	label := pin.BoardLabel
	if pin.PackageLabel != "" {
		label = fmt.Sprintf("%s/%s", pin.BoardLabel, pin.PackageLabel)
	}
	for _, pd := range pin.ParentDevice {
		prioStr := "n/a"
		if pd.Prio != nil {
			prioStr = fmt.Sprintf("%d", *pd.Prio)
		}
		glog.Infof("  pin=%-3d 0x%x %-20s parentID=%-2d dir=%-6s prio=%-4s admin=%-12s oper=%s",
			pin.ID, pin.ClockID, label, pd.ParentID,
			GetPinDirection(pd.Direction), prioStr,
			GetPinState(pd.State), GetPinOperstate(pd.Operstate))
	}
}

func hasRelevantParent(pin *PinInfo) bool {
	for _, pd := range pin.ParentDevice {
		if pd.State == PinStateSelectable || pd.State == PinStateConnected ||
			pd.Operstate == PinOperstateActive {
			return true
		}
	}
	return false
}
