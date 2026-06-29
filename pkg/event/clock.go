package event

import (
	"context"
	"fmt"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
)

// Clock represents a PTP clock instance tied to a specific config profile.
type Clock interface {
	ClockType() ClockType
	ConfigName() string
	ClockClass() fbprotocol.ClockClass
	SystemClockUpdate(state PTPState)
	ParentDSUpdate(parentDS protocol.ParentDataSet)
}

// clockIO is the I/O interface that clock objects use for external side effects.
type clockIO interface {
	Lock()
	Unlock()
	emitClockClass(clockClass fbprotocol.ClockClass, cfgName string)
	sendIPC(msg ipc.Message)
	getGMSettings(cfgName string) (protocol.GrandmasterSettings, error)
	setGMSettings(cfgName string, g protocol.GrandmasterSettings) error
	setExternalGMPropertiesNP(cfgName string, egp protocol.ExternalGrandmasterProperties) error
	getParentTimeAndCurrentDS(cfgName string) (pmc.ParentTimeCurrentDS, error)
	getUtcOffset() int
}

// GMClock is a Grand Master clock instance.
// It owns its sync state and emits IPC messages via clockIO.
type GMClock struct {
	cfgName                string
	io                     clockIO
	data                   []*Data
	syncState              clockSyncState
	overallSyncState       PTPState
	osClockState           PTPState
	gnssState              PTPState
	announcedClockClass    fbprotocol.ClockClass
	announcedClockAccuracy fbprotocol.ClockAccuracy
	lastLoggedState        PTPState
}

// ClockType returns GM.
func (c *GMClock) ClockType() ClockType { return GM }

func (c *GMClock) ClockClass() fbprotocol.ClockClass {
	return c.syncState.clockClass
}

// ConfigName returns the config profile name.
func (c *GMClock) ConfigName() string { return c.cfgName }

// TBCClock is a Boundary Clock instance.
// It owns its sync state and event data. State-machine methods
// (addEvent, updateBCState, helpers) use only struct fields.
// I/O methods (announceLocalData, etc.) use the injected clockIO interface.
type TBCClock struct {
	cfgName                string
	io                     clockIO
	syncState              clockSyncState
	overallSyncState       PTPState
	osClockState           PTPState
	announcedClockClass    fbprotocol.ClockClass
	announcedClockAccuracy fbprotocol.ClockAccuracy
	data                   []*Data
	leadingClockData       *LeadingClockParams
	downstreamCancel       context.CancelFunc
}

// ClockType returns TBC.
func (c *TBCClock) ClockType() ClockType { return TBC }

func (c *TBCClock) ClockClass() fbprotocol.ClockClass {
	return c.syncState.clockClass
}

// ConfigName returns the config profile name.
func (c *TBCClock) ConfigName() string { return c.cfgName }

// OCClock is an Ordinary Clock instance.
type OCClock struct {
	cfgName string
}

// ClockType returns OC.
func (c *OCClock) ClockType() ClockType { return OC }

func (c *OCClock) ClockClass() fbprotocol.ClockClass { return c.ClockClass() }

func (c *OCClock) SystemClockUpdate(_ PTPState) {}

func (c *OCClock) ParentDSUpdate(_ protocol.ParentDataSet) {}

// ConfigName returns the config profile name.
func (c *OCClock) ConfigName() string { return c.cfgName }

// newClock creates the appropriate Clock implementation for the given clock type.
func newClock(cfgName string, clockType ClockType, io clockIO) (Clock, error) {
	switch clockType {
	case GM:
		return &GMClock{
			cfgName: cfgName,
			io:      io,
			syncState: clockSyncState{
				state:         PTP_NOTSET,
				clockClass:    protocol.ClockClassUninitialized,
				clockAccuracy: fbprotocol.ClockAccuracyUnknown,
			},
			overallSyncState:       PTP_NOTSET,
			osClockState:           PTP_NOTSET,
			gnssState:              PTP_NOTSET,
			announcedClockClass:    protocol.ClockClassUninitialized,
			announcedClockAccuracy: fbprotocol.ClockAccuracyUnknown,
		}, nil
	case TBC:
		return &TBCClock{
			cfgName: cfgName,
			io:      io,
			syncState: clockSyncState{
				state:         PTP_NOTSET,
				clockClass:    protocol.ClockClassUninitialized,
				clockAccuracy: fbprotocol.ClockAccuracyUnknown,
			},
			overallSyncState:       PTP_NOTSET,
			osClockState:           PTP_NOTSET,
			announcedClockClass:    protocol.ClockClassUninitialized,
			announcedClockAccuracy: fbprotocol.ClockAccuracyUnknown,
			leadingClockData:       newLeadingClockParams(),
		}, nil
	case BC:
		return &BCClock{
			cfgName:          cfgName,
			io:               io,
			syncState:        PTP_NOTSET,
			overallSyncState: PTP_NOTSET,
			osClockState:     PTP_NOTSET,
		}, nil
	case OC:
		return &OCClock{cfgName: cfgName}, nil
	default:
		return nil, fmt.Errorf("unsupported clock type %q for config %s", clockType, cfgName)
	}
}
