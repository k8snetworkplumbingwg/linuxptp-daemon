package daemon

import (
	"context"
	"os/exec"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ublox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGPSDIsOffsetInRange covers isOffsetInRange's abs(offset) <
// GMThreshold.Max comparison, including that a nonzero/negative
// GMThreshold.Min (deprecated) has no effect on the result.
func TestGPSDIsOffsetInRange(t *testing.T) {
	tests := []struct {
		name      string
		offset    int64
		threshold config.Threshold
		expected  bool
	}{
		{name: "in-range positive offset -> true", offset: 50, threshold: config.Threshold{Max: 100}, expected: true},
		{name: "in-range negative offset -> true", offset: -50, threshold: config.Threshold{Max: 100}, expected: true},
		{name: "out-of-range positive offset -> false", offset: 150, threshold: config.Threshold{Max: 100}, expected: false},
		{name: "out-of-range negative offset -> false", offset: -150, threshold: config.Threshold{Max: 100}, expected: false},
		{name: "exact boundary offset (non-inclusive) -> false", offset: 100, threshold: config.Threshold{Max: 100}, expected: false},
		{
			name:      "backward-compat: nonzero Min is ignored, negative offset within Max -> true",
			offset:    -80,
			threshold: config.Threshold{Max: 100, Min: -50},
			expected:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &GPSD{
				offset:        tt.offset,
				processConfig: config.ProcessConfig{GMThreshold: tt.threshold},
			}
			assert.Equal(t, tt.expected, g.isOffsetInRange(), tt.name)
		})
	}
}

func TestResetSerialPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		serialPort string
		runner     func(ctx context.Context, name string, args ...string) *exec.Cmd
		wantErr    bool
	}{
		{
			name:       "happy path: stty succeeds",
			serialPort: "/dev/ttyUSB0",
			runner: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
				return exec.CommandContext(ctx, "true")
			},
			wantErr: false,
		},
		{
			name:       "stty command fails",
			serialPort: "/dev/ttyUSB0",
			runner: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
				return exec.CommandContext(ctx, "false")
			},
			wantErr: true,
		},
		{
			name:       "empty serialPort skips reset without error",
			serialPort: "",
			runner: func(_ context.Context, _ string, _ ...string) *exec.Cmd {
				t.Error("cmdRunner must not be called when serialPort is empty")
				return nil
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := &GPSD{
				serialPort: tt.serialPort,
				cmdRunner:  tt.runner,
			}
			err := g.resetSerialPort(context.Background())
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestResetSerialPortPassesCorrectArgs(t *testing.T) {
	t.Parallel()

	const device = "/dev/ttyS0"
	var capturedName string
	var capturedArgs []string

	g := &GPSD{
		serialPort: device,
		cmdRunner: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			capturedName = name
			capturedArgs = args
			return exec.CommandContext(ctx, "true")
		},
	}

	err := g.resetSerialPort(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, "stty", capturedName)
	assert.Equal(t, []string{"-F", device, "sane"}, capturedArgs)
}

func TestProcessGNSSMessageCorrelatesLatestStatusAndClock(t *testing.T) {
	eventCh := make(chan event.Event, 1)
	g := &GPSD{
		processConfig: config.ProcessConfig{
			EventChannel: eventCh,
			GMThreshold:  config.Threshold{Max: 100},
		},
	}

	// Keep an unmatched clock until a status with the same iTOW arrives.
	assert.False(t, g.processGNSSMessage(ublox.Message{
		Type:    ublox.NavClockType,
		Payload: ublox.NavClock{Offset: 10, ITOW: 100},
	}))
	assert.Empty(t, eventCh)

	// A newer status replaces the previous correlation candidate.
	assert.False(t, g.processGNSSMessage(ublox.Message{
		Type:    ublox.NavStatusType,
		Payload: ublox.NavStatus{GPSFix: 3, ITOW: 101},
	}))
	assert.Empty(t, eventCh)

	assert.True(t, g.processGNSSMessage(ublox.Message{
		Type:    ublox.NavClockType,
		Payload: ublox.NavClock{Offset: 10, ITOW: 101},
	}))
	assert.Nil(t, g.lastNavStatus)
	assert.Nil(t, g.lastNavClock)
	assert.Len(t, eventCh, 1)
	eventValue := <-eventCh
	gnssData, ok := eventValue.Data.(*event.GNSSData)
	require.True(t, ok)
	assert.Equal(t, int64(3), gnssData.GPSStatus)
	assert.Equal(t, int64(10), gnssData.Offset)
}

func TestProcessGNSSMessageCorrelatesClockBeforeStatus(t *testing.T) {
	eventCh := make(chan event.Event, 1)
	g := &GPSD{
		processConfig: config.ProcessConfig{
			EventChannel: eventCh,
			GMThreshold:  config.Threshold{Max: 100},
		},
	}

	assert.False(t, g.processGNSSMessage(ublox.Message{
		Type:    ublox.NavClockType,
		Payload: ublox.NavClock{Offset: 10, ITOW: 100},
	}))
	assert.NotNil(t, g.lastNavClock)
	assert.Empty(t, eventCh)

	assert.True(t, g.processGNSSMessage(ublox.Message{
		Type:    ublox.NavStatusType,
		Payload: ublox.NavStatus{GPSFix: 3, ITOW: 100},
	}))
	assert.Nil(t, g.lastNavStatus)
	assert.Nil(t, g.lastNavClock)

	eventValue := <-eventCh
	gnssData, ok := eventValue.Data.(*event.GNSSData)
	require.True(t, ok)
	assert.Equal(t, int64(3), gnssData.GPSStatus)
	assert.Equal(t, int64(10), gnssData.Offset)
}

func TestProcessGNSSMessageRejectsClockWithoutMatchingStatus(t *testing.T) {
	eventCh := make(chan event.Event, 1)
	g := &GPSD{
		processConfig: config.ProcessConfig{
			EventChannel: eventCh,
			GMThreshold:  config.Threshold{Max: 100},
		},
	}

	assert.False(t, g.processGNSSMessage(ublox.Message{
		Type:    ublox.NavStatusType,
		Payload: ublox.NavStatus{GPSFix: 3, ITOW: 100},
	}))
	assert.NotNil(t, g.lastNavStatus)

	assert.True(t, g.processGNSSMessage(ublox.Message{
		Type:    ublox.NavClockType,
		Payload: ublox.NavClock{Offset: 10, ITOW: 100},
	}))
	assert.Nil(t, g.lastNavStatus)
	assert.Nil(t, g.lastNavClock)
	assert.Len(t, eventCh, 1)
	<-eventCh

	// The status for iTOW 100 has already been consumed, so a later clock
	// cannot reuse its GPSFix value without a new matching status.
	assert.False(t, g.processGNSSMessage(ublox.Message{
		Type:    ublox.NavClockType,
		Payload: ublox.NavClock{Offset: 20, ITOW: 101},
	}))
	assert.Empty(t, eventCh)
}
