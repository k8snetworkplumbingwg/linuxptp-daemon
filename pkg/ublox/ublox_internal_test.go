package ublox

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_BatchMsgoutAllBusses(t *testing.T) {
	msgs := []string{navEnableMsg[0]} // single message
	cmd := batchMsgoutAllBusses("UBX_NAV", msgs, 1)
	// Should produce one -z pair per bus type
	assert.Equal(t, len(ublxBusTypes)*2, len(cmd.Args))
	for _, bus := range ublxBusTypes {
		expected := fmt.Sprintf("CFG-MSGOUT-UBX_NAV_%s_%s,1", msgs[0], bus)
		assert.Contains(t, cmd.Args, expected)
	}
}

func Test_BatchMsgoutAllBusses_MultipleMessages(t *testing.T) {
	cmd := batchMsgoutAllBusses("UBX_NAV", navEnableMsg, 1)
	// N messages × 5 bus types × 2 args each (-z + value)
	assert.Equal(t, len(navEnableMsg)*len(ublxBusTypes)*2, len(cmd.Args))
	for _, msg := range navEnableMsg {
		for _, bus := range ublxBusTypes {
			expected := fmt.Sprintf("CFG-MSGOUT-UBX_NAV_%s_%s,1", msg, bus)
			assert.Contains(t, cmd.Args, expected)
		}
	}
}

func Test_BatchDisableNmeaMsgs(t *testing.T) {
	cmd := batchDisableNmeaMsgs([]string{"FOO", "BAR"})
	for _, msg := range []string{"FOO", "BAR"} {
		for _, bus := range ublxBusTypes {
			expected := fmt.Sprintf("CFG-MSGOUT-NMEA_ID_%s_%s,0", msg, bus)
			assert.Contains(t, cmd.Args, expected)
		}
	}
}

func Test_BatchEnableNavMsgs(t *testing.T) {
	cmd := batchEnableNavMsgs(navEnableMsg)
	for _, msg := range navEnableMsg {
		for _, bus := range ublxBusTypes {
			expected := fmt.Sprintf("CFG-MSGOUT-UBX_NAV_%s_%s,1", msg, bus)
			assert.Contains(t, cmd.Args, expected)
		}
	}
}

func Test_DefaultUblxCmds(t *testing.T) {
	cmds := defaultUblxCmds()

	// Flatten all args for easy searching
	var allArgs []string
	for _, cmd := range cmds {
		allArgs = append(allArgs, cmd.Args...)
	}

	// First command should disable all binary
	assert.Equal(t, disableBinary, cmds[0])

	// All NAV messages should be re-enabled on all bus types
	for _, nav := range navEnableMsg {
		for _, bus := range ublxBusTypes {
			expected := fmt.Sprintf("CFG-MSGOUT-UBX_NAV_%s_%s,1", nav, bus)
			assert.Contains(t, allArgs, expected,
				"expected NAV %s enable on %s", nav, bus)
		}
	}

	// NMEA should be enabled
	assert.Contains(t, cmds, enableNMEA)

	// All NMEA disable messages should be present on all bus types
	for _, nmea := range nmeaDisableMsg {
		for _, bus := range ublxBusTypes {
			expected := fmt.Sprintf("CFG-MSGOUT-NMEA_ID_%s_%s,0", nmea, bus)
			assert.Contains(t, allArgs, expected,
				"expected NMEA %s disable on %s", nmea, bus)
		}
	}
}
