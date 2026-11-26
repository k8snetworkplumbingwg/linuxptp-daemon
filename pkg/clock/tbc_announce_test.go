package clock

import (
	"strconv"
	"sync"
	"testing"
	"time"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type delayedAnnounceClient struct {
	*pmc.MockClient
	blockedClass    fbprotocol.ClockClass
	blockedIdentity string
	entered         chan struct{}
	release         chan struct{}
	completed       chan fbprotocol.ClockClass
	once            sync.Once
}

func (c *delayedAnnounceClient) SetExternalGMPropertiesNP(cfg string, properties protocol.ExternalGrandmasterProperties) error {
	if c.blockedIdentity != "" && properties.GrandmasterIdentity == c.blockedIdentity {
		c.once.Do(func() { close(c.entered) })
		<-c.release
	}
	return c.MockClient.SetExternalGMPropertiesNP(cfg, properties)
}

func (c *delayedAnnounceClient) SetGMSettings(cfg string, settings protocol.GrandmasterSettings) error {
	if c.blockedClass != 0 && settings.ClockQuality.ClockClass == c.blockedClass {
		c.once.Do(func() { close(c.entered) })
		<-c.release
	}
	err := c.MockClient.SetGMSettings(cfg, settings)
	c.completed <- settings.ClockQuality.ClockClass
	return err
}

func recoveredAnnounce(bc *TBC) *event.ParentTimeCurrentDS {
	return &event.ParentTimeCurrentDS{
		Generation: bc.announceToken,
		ParentTimeCurrentDS: pmc.ParentTimeCurrentDS{
			ParentDataSet: protocol.ParentDataSet{GrandmasterClockClass: 6, GrandmasterIdentity: "external-clock"},
			TimePropertiesDS: protocol.TimePropertiesDS{
				CurrentUtcOffset: 37, CurrentUtcOffsetValid: true, PtpTimescale: true, TimeTraceable: true,
			},
			CurrentDS: protocol.CurrentDS{StepsRemoved: 1},
		},
	}
}

func waitForDownstreamWrites(t *testing.T, bc *TBC) {
	t.Helper()
	bc.downstreamWrites.mu.Lock()
	done := bc.downstreamWrites.workerDone
	bc.downstreamWrites.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("downstream writer did not become idle")
		}
	}
}

func TestDownstreamAnnounceWritesStayOrderedAcrossRecovery(t *testing.T) {
	for _, clockClass := range []fbprotocol.ClockClass{135, 165, protocol.ClockClassFreerun} {
		t.Run(strconv.Itoa(int(clockClass)), func(t *testing.T) {
			client := &delayedAnnounceClient{
				MockClient: &pmc.MockClient{}, blockedClass: clockClass,
				entered: make(chan struct{}), release: make(chan struct{}), completed: make(chan fbprotocol.ClockClass, 2),
			}
			var once sync.Once
			unblock := func() { once.Do(func() { close(client.release) }) }
			bc := newPMCTestTBCClock(client)
			t.Cleanup(func() { unblock(); bc.Close() })
			bc.controlledPortsConfig = testControlledCfg
			bc.leadingClockData.clockID = "local-clock"
			bc.syncState = SyncState{State: event.PTP_HOLDOVER, ClockClass: clockClass}
			bc.announceLocalData(testCfgName)
			select {
			case <-client.entered:
			case <-time.After(time.Second):
				t.Fatal("local GM-settings write did not start")
			}

			// Recovery must remain responsive even while the old PMC call is
			// blocked. The old call must finish before recovery settings apply.
			bc.syncState = SyncState{State: event.PTP_LOCKED, ClockClass: fbprotocol.ClockClass6}
			message := recoveredAnnounce(bc)
			handled := make(chan struct{})
			go func() {
				defer close(handled)
				bc.handleDownstreamAnnounceIWF(message)
			}()
			t.Cleanup(func() {
				unblock()
				select {
				case <-handled:
				case <-time.After(time.Second):
					t.Error("recovery handler did not stop")
				}
			})
			select {
			case <-handled:
			case <-time.After(time.Second):
				t.Fatal("PMC I/O blocked the recovery state handler")
			}
			unblock()
			var completed []fbprotocol.ClockClass
			for range 2 {
				select {
				case applied := <-client.completed:
					completed = append(completed, applied)
				case <-time.After(time.Second):
					t.Fatal("downstream settings were not applied")
				}
			}
			require.Equal(t, []fbprotocol.ClockClass{clockClass, fbprotocol.ClockClass6}, completed,
				"a delayed local write must not overwrite recovery settings")
			calls := client.SnapshotSetCalls()
			require.Len(t, calls, 4)
			require.Equal(t, "SetExternalGMPropertiesNP", calls[0].Method)
			require.Equal(t, "local-clock", calls[0].ExternalGMPropertiesNP.GrandmasterIdentity)
			require.Equal(t, "SetGMSettings", calls[1].Method)
			require.Equal(t, "SetExternalGMPropertiesNP", calls[2].Method)
			require.Equal(t, "external-clock", calls[2].ExternalGMPropertiesNP.GrandmasterIdentity)
			require.Equal(t, "SetGMSettings", calls[3].Method)
			for _, call := range calls {
				require.Equal(t, testControlledCfg, call.CfgName, "only the downstream instance may be updated")
			}
		})
	}
}

func TestDownstreamAnnounceLatestPendingStateWins(t *testing.T) {
	client := &delayedAnnounceClient{
		MockClient: &pmc.MockClient{}, blockedIdentity: "local-clock",
		entered: make(chan struct{}), release: make(chan struct{}), completed: make(chan fbprotocol.ClockClass, 3),
	}
	var once sync.Once
	unblock := func() { once.Do(func() { close(client.release) }) }
	bc := newPMCTestTBCClock(client)
	t.Cleanup(func() { unblock(); bc.Close() })
	bc.controlledPortsConfig = testControlledCfg
	bc.leadingClockData.clockID = "local-clock"
	bc.syncState = SyncState{State: event.PTP_HOLDOVER, ClockClass: 135}
	bc.announceLocalData(testCfgName)
	select {
	case <-client.entered:
	case <-time.After(time.Second):
		t.Fatal("local EGP write did not start")
	}
	bc.leadingClockData.clockID = "superseded-clock"
	bc.syncState.ClockClass = 165
	bc.announceLocalData(testCfgName)
	bc.syncState = SyncState{State: event.PTP_LOCKED, ClockClass: fbprotocol.ClockClass6}
	bc.handleDownstreamAnnounceIWF(recoveredAnnounce(bc))
	unblock()
	waitForDownstreamWrites(t, bc)
	calls := client.SnapshotSetCalls()
	require.Len(t, calls, 3, "obsolete local settings and intermediate pending states must be skipped")
	require.Equal(t, "local-clock", calls[0].ExternalGMPropertiesNP.GrandmasterIdentity)
	require.Equal(t, "external-clock", calls[1].ExternalGMPropertiesNP.GrandmasterIdentity)
	require.Equal(t, fbprotocol.ClockClass6, calls[2].GMSettings.ClockQuality.ClockClass)
}

func TestDownstreamAnnounceResetDiscardsPendingWrites(t *testing.T) {
	client := &delayedAnnounceClient{
		MockClient: &pmc.MockClient{}, blockedIdentity: "local-clock",
		entered: make(chan struct{}), release: make(chan struct{}), completed: make(chan fbprotocol.ClockClass, 3),
	}
	var once sync.Once
	unblock := func() { once.Do(func() { close(client.release) }) }
	bc := newPMCTestTBCClock(client)
	t.Cleanup(func() { unblock(); bc.Close() })
	bc.controlledPortsConfig = testControlledCfg
	bc.leadingClockData.clockID = "local-clock"
	bc.syncState = SyncState{State: event.PTP_HOLDOVER, ClockClass: 135}
	bc.announceLocalData(testCfgName)
	select {
	case <-client.entered:
	case <-time.After(time.Second):
		t.Fatal("local EGP write did not start")
	}
	bc.syncState.ClockClass = 165
	bc.announceLocalData(testCfgName)
	bc.Reset()
	unblock()
	waitForDownstreamWrites(t, bc)
	require.Empty(t, filterSetCalls(client.SnapshotSetCalls(), "SetGMSettings"), "reset must invalidate both active and pending old settings")
	bc.syncState = SyncState{State: event.PTP_LOCKED, ClockClass: fbprotocol.ClockClass6}
	bc.handleDownstreamAnnounceIWF(recoveredAnnounce(bc))
	waitForDownstreamWrites(t, bc)
	calls := filterSetCalls(client.SnapshotSetCalls(), "SetGMSettings")
	require.Len(t, calls, 1, "the reset clock must accept announcements from its new epoch")
	require.Equal(t, fbprotocol.ClockClass6, calls[0].GMSettings.ClockQuality.ClockClass)
}

func TestDownstreamAnnounceCloseDrainsActiveWrite(t *testing.T) {
	client := &delayedAnnounceClient{
		MockClient: &pmc.MockClient{}, blockedClass: 135,
		entered: make(chan struct{}), release: make(chan struct{}), completed: make(chan fbprotocol.ClockClass, 3),
	}
	var once sync.Once
	unblock := func() { once.Do(func() { close(client.release) }) }
	bc := newPMCTestTBCClock(client)
	t.Cleanup(func() { unblock(); bc.Close() })
	bc.controlledPortsConfig = testControlledCfg
	bc.leadingClockData.clockID = "local-clock"
	bc.syncState = SyncState{State: event.PTP_HOLDOVER, ClockClass: 135}
	bc.announceLocalData(testCfgName)
	select {
	case <-client.entered:
	case <-time.After(time.Second):
		t.Fatal("local GM-settings write did not start")
	}
	bc.syncState.ClockClass = 165
	bc.announceLocalData(testCfgName)
	closed := make(chan struct{})
	go func() { bc.Close(); close(closed) }()
	require.Eventually(t, func() bool {
		bc.downstreamWrites.mu.Lock()
		defer bc.downstreamWrites.mu.Unlock()
		return bc.downstreamWrites.closed
	}, time.Second, time.Millisecond)
	select {
	case <-closed:
		t.Fatal("clock retirement did not wait for the in-flight PMC write")
	default:
	}
	bc.announceLocalData(testCfgName) // A retired owner cannot enqueue more work.
	unblock()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("clock retirement did not finish after the PMC write completed")
	}
	calls := filterSetCalls(client.SnapshotSetCalls(), "SetGMSettings")
	require.Len(t, calls, 1, "retirement must discard pending and subsequent announcements")
	require.Equal(t, fbprotocol.ClockClass(135), calls[0].GMSettings.ClockQuality.ClockClass)
}

func TestDownstreamAnnounceWriterContinuesAfterErrors(t *testing.T) {
	client := &pmc.MockClient{SetExternalGMPropertiesNPErr: assert.AnError, SetGMSettingsErr: assert.AnError}
	bc := newPMCTestTBCClock(client)
	t.Cleanup(bc.Close)
	bc.controlledPortsConfig = testControlledCfg
	bc.leadingClockData.clockID = "local-clock"
	bc.syncState = SyncState{State: event.PTP_HOLDOVER, ClockClass: 135}
	bc.announceLocalData(testCfgName)
	waitForDownstreamWrites(t, bc)
	bc.downstreamWrites.mu.Lock()
	done := bc.downstreamWrites.workerDone
	bc.downstreamWrites.mu.Unlock()
	require.Nil(t, done, "the completed worker must become idle")
	bc.syncState = SyncState{State: event.PTP_LOCKED, ClockClass: fbprotocol.ClockClass6}
	bc.handleDownstreamAnnounceIWF(recoveredAnnounce(bc))
	waitForDownstreamWrites(t, bc)
	calls := client.SnapshotSetCalls()
	require.Len(t, calls, 4, "a failed setter must not strand the writer or skip subsequent setters")
	require.Equal(t, "SetGMSettings", calls[1].Method)
	require.Equal(t, "SetGMSettings", calls[3].Method)
	require.Equal(t, fbprotocol.ClockClass6, calls[3].GMSettings.ClockQuality.ClockClass)
}
