package event

import fbprotocol "github.com/facebook/time/ptp/protocol"

// EventHandlerForTests wraps EventHandler with test-only helpers
// that access unexported fields. This keeps test utilities out of
// production code.
type EventHandlerForTests struct {
	*EventHandler
	bc *BCClock
}

// NewEventHandlerForTests creates an EventHandlerForTests wrapping
// a minimal EventHandler for socket logic tests.
func NewEventHandlerForTests(socketPath string) *EventHandlerForTests {
	handler := newTestEventHandler(socketPath)
	bc := &BCClock{
		io:               handler,
		leadingClockData: newLeadingClockParams(),
	}
	return &EventHandlerForTests{
		EventHandler: handler,
		bc:           bc,
	}
}

// SetClockClass stores the clock class in clkSyncState so EmitClockClass has data to emit.
func (t *EventHandlerForTests) SetClockClass(cfgName string, clockClass uint8) {
	t.Lock()
	defer t.Unlock()
	t.storeClockClassLocked(cfgName, fbprotocol.ClockClass(clockClass), fbprotocol.ClockAccuracyUnknown)
}

// EmitClockClass delegates to BCClock.EmitClockClass for socket tests.
func (t *EventHandlerForTests) EmitClockClass(cfgName string) {
	t.bc.EmitClockClass(cfgName)
}
