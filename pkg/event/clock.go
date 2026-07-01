package event

import (
	"context"
	"fmt"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
)

// Clock represents a PTP clock instance tied to a specific config profile.
type Clock interface {
	ClockType() ClockType
	ConfigName() string
}

// clockIO is the I/O interface that BCClock uses for locking and clock-class announcements.
type clockIO interface {
	Lock()
	Unlock()
	announceClockClass(clockClass fbprotocol.ClockClass, clockAccuracy fbprotocol.ClockAccuracy, cfgName string)
	emitClockClass(clockClass fbprotocol.ClockClass, cfgName string)
	getStoredClockClass(cfgName string) (fbprotocol.ClockClass, bool)
	updateDownstreamData(bc *BCClock, cfgName string)
	sendIPC(msg ipc.Message)
}

// BCProcessResult holds the outcome of BCClock.addEvent.
type BCProcessResult struct {
	ClockState       clockSyncState
	SyncStateChanged bool
	OverallSyncState PTPState
}

// GMClock is a Grand Master clock instance.
type GMClock struct {
	cfgName string
}

// ClockType returns GM.
func (c *GMClock) ClockType() ClockType { return GM }

// ConfigName returns the config profile name.
func (c *GMClock) ConfigName() string { return c.cfgName }

// BCClock is a Boundary Clock instance.
// It owns its sync state and event data. State-machine methods
// (addEvent, updateBCState, helpers) use only struct fields.
// I/O methods (announceLocalData, etc.) use the injected clockIO interface.
type BCClock struct {
	cfgName          string
	io               clockIO
	syncState        clockSyncState
	overallSyncState PTPState
	data             []*Data
	leadingClockData *LeadingClockParams
	downstreamCancel context.CancelFunc
}

// ClockType returns BC.
func (c *BCClock) ClockType() ClockType { return BC }

// ConfigName returns the config profile name.
func (c *BCClock) ConfigName() string { return c.cfgName }

// OCClock is an Ordinary Clock instance.
type OCClock struct {
	cfgName string
}

// ClockType returns OC.
func (c *OCClock) ClockType() ClockType { return OC }

// ConfigName returns the config profile name.
func (c *OCClock) ConfigName() string { return c.cfgName }

// newClock creates the appropriate Clock implementation for the given clock type.
func newClock(cfgName string, clockType ClockType, io clockIO) (Clock, error) {
	switch clockType {
	case GM:
		return &GMClock{cfgName: cfgName}, nil
	case BC:
		return &BCClock{
			cfgName: cfgName,
			io:      io,
			syncState: clockSyncState{
				state:         PTP_FREERUN,
				clockClass:    protocol.ClockClassUninitialized,
				clockAccuracy: fbprotocol.ClockAccuracyUnknown,
			},
			overallSyncState: PTP_FREERUN,
			leadingClockData: newLeadingClockParams(),
		}, nil
	case OC:
		return &OCClock{cfgName: cfgName}, nil
	default:
		return nil, fmt.Errorf("unsupported clock type %q for config %s", clockType, cfgName)
	}
}
