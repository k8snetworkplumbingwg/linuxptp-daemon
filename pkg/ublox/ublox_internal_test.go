package ublox

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_CmdMsgoutAllBusses(t *testing.T) {
	cmds := cmdMsgoutAllBusses("UBX_NAV", "CLOCK", 1)
	assert.Equal(t, len(ublxBusTypes), len(cmds))
	for i, bus := range ublxBusTypes {
		assert.Equal(t, []string{"-z", "CFG-MSGOUT-UBX_NAV_CLOCK_" + bus + ",1"}, cmds[i].Args)
	}
}

func Test_CmdDisableNmeaMsg(t *testing.T) {
	expected := []string{
		"CFG-MSGOUT-NMEA_ID_FOO_I2C,0",
		"CFG-MSGOUT-NMEA_ID_FOO_UART1,0",
		"CFG-MSGOUT-NMEA_ID_FOO_UART2,0",
		"CFG-MSGOUT-NMEA_ID_FOO_USB,0",
		"CFG-MSGOUT-NMEA_ID_FOO_SPI,0",
	}
	found := make([]string, 0, len(expected))
	cmds := cmdDisableNmeaMsg("FOO")
	assert.Equal(t, len(expected), len(cmds))
	for _, cmd := range cmds {
		assert.Equal(t, "-z", cmd.Args[0])
		if slices.Contains(expected, cmd.Args[1]) {
			found = append(found, cmd.Args[1])
		}
	}
	slices.Sort(expected)
	slices.Sort(found)
	assert.Equal(t, expected, found)
}

func Test_CmdEnableNavMsg(t *testing.T) {
	expected := []string{
		"CFG-MSGOUT-UBX_NAV_STATUS_I2C,1",
		"CFG-MSGOUT-UBX_NAV_STATUS_UART1,1",
		"CFG-MSGOUT-UBX_NAV_STATUS_UART2,1",
		"CFG-MSGOUT-UBX_NAV_STATUS_USB,1",
		"CFG-MSGOUT-UBX_NAV_STATUS_SPI,1",
	}
	found := make([]string, 0, len(expected))
	cmds := cmdEnableNavMsg("STATUS")
	assert.Equal(t, len(expected), len(cmds))
	for _, cmd := range cmds {
		assert.Equal(t, "-z", cmd.Args[0])
		if slices.Contains(expected, cmd.Args[1]) {
			found = append(found, cmd.Args[1])
		}
	}
	slices.Sort(expected)
	slices.Sort(found)
	assert.Equal(t, expected, found)
}
