package event

import fbprotocol "github.com/facebook/time/ptp/protocol"

// EventHandlerForTests wraps EventHandler with test-only helpers
// that access unexported fields. This keeps test utilities out of
// production code.
type EventHandlerForTests struct {
	*EventHandler
}

// NewEventHandlerForTests creates an EventHandlerForTests wrapping
// a minimal EventHandler for socket logic tests.
func NewEventHandlerForTests(socketPath string) *EventHandlerForTests {
	handler := newTestEventHandler(socketPath)
	return &EventHandlerForTests{
		EventHandler: handler,
	}
}

// SetClockClass registers a TBC clock with the given announced clock class.
func (t *EventHandlerForTests) SetClockClass(cfgName string, clockClass uint8) {
	t.Lock()
	defer t.Unlock()
	if t.clocks == nil {
		t.clocks = map[string]Clock{}
	}
	t.clocks[cfgName] = &TBCClock{
		cfgName:             cfgName,
		io:                  t.EventHandler,
		announcedClockClass: fbprotocol.ClockClass(clockClass),
		leadingClockData:    newLeadingClockParams(),
	}
}

// EmitClockClass delegates to EventHandler.EmitClockClass.
func (t *EventHandlerForTests) EmitClockClass(cfgName string) {
	t.EventHandler.EmitClockClass(cfgName)
}
