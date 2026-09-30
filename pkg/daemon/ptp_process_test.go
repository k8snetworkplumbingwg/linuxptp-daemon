package daemon

import (
	"testing"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
)

// TestGetPTPClockId_ValidInput tests the getPTPClockID method with valid input.
// It verifies that a valid clock ID configuration is properly parsed and formatted.
func TestGetPTPClockId_ValidInput(t *testing.T) {
	p := &ptpProcess{
		nodeProfile: &ptpv1.PtpProfile{
			PtpSettings: map[string]string{
				"leadingInterface": "eth0",
				"clockId[eth0]":    "123456",
			},
		},
	}

	expectedClockID := "000000.fffe.01e240"
	actualClockID, err := p.getPTPClockID()
	assert.NoError(t, err)
	assert.Equal(t, expectedClockID, actualClockID)
}

// TestGetPTPClockId_ParsingError tests the getPTPClockID method with invalid clock ID value.
// It verifies that an error is returned when the clock ID cannot be parsed as a number.
func TestGetPTPClockId_ParsingError(t *testing.T) {
	p := &ptpProcess{
		nodeProfile: &ptpv1.PtpProfile{
			PtpSettings: map[string]string{
				"leadingInterface": "eth0",
				"clockId[eth0]":    "invalid_string",
			},
		},
	}

	_, err := p.getPTPClockID()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse clock ID string invalid_string")
}

// TestGetPTPClockId_MissingClockId tests the getPTPClockID method when the clock ID setting is missing.
// It verifies that an error is returned when the required clock ID configuration is not found.
func TestGetPTPClockId_MissingClockId(t *testing.T) {
	p := &ptpProcess{
		nodeProfile: &ptpv1.PtpProfile{
			PtpSettings: map[string]string{
				"leadingInterface": "eth0",
			},
		},
	}

	_, err := p.getPTPClockID()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "leading interface ClockId not found in ptpProfile")
}

// TestGetPTPClockId_MissingLeadingInterface tests the getPTPClockID method when the leading interface setting is missing.
// It verifies that an error is returned when the required leading interface configuration is not found.
func TestGetPTPClockId_MissingLeadingInterface(t *testing.T) {
	p := &ptpProcess{
		nodeProfile: &ptpv1.PtpProfile{
			PtpSettings: map[string]string{},
		},
	}

	_, err := p.getPTPClockID()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "leadingInterface not found in ptpProfile")
}
