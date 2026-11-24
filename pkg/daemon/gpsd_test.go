package daemon

import (
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
	"testing"
)

const ttestTs2phcConfig = "ts2phc.0.config"

// TestProcessConfigForCopiesThresholds verifies process thresholds are copied to the config.
func TestProcessConfigForCopiesThresholds(t *testing.T) {
	ch := make(chan event.Event, 1)
	p := &ptpProcess{
		ExecProcess: ExecProcess{configName: ttestTs2phcConfig},
		ptpClockThreshold: &ptpv1.PtpClockThreshold{
			MaxOffsetThreshold: 100,
			MinOffsetThreshold: -100,
			HoldOverTimeout:    5,
		},
	}
	cfg := processConfigFor(p, event.TGM, ch)
	assert.Equal(t, event.TGM, cfg.ClockType)
	assert.Equal(t, ttestTs2phcConfig, cfg.ConfigName)
	assert.Equal(t, int64(100), cfg.GMThreshold.Max)
	assert.Equal(t, int64(-100), cfg.GMThreshold.Min)
	assert.Equal(t, int64(5), cfg.GMThreshold.HoldOverTimeout)
	assert.Equal(t, event.PTP_FREERUN, cfg.InitialPTPState)
}
