package clockmgr

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/alias"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

type managerPMCClient struct {
	pmc.MockClient
	blocked string
	once    sync.Once
	entered chan context.Context
}

func (client *managerPMCClient) wait(ctx context.Context, kind string) error {
	if kind != client.blocked {
		return nil
	}
	client.once.Do(func() { client.entered <- ctx })
	<-ctx.Done()
	return ctx.Err()
}
func (client *managerPMCClient) GetGMSettingsContext(ctx context.Context, config string) (protocol.GrandmasterSettings, error) {
	if err := client.wait(ctx, "gm-get"); err != nil {
		return protocol.GrandmasterSettings{}, err
	}
	return client.MockClient.GetGMSettingsContext(ctx, config)
}
func (client *managerPMCClient) SetGMSettingsContext(ctx context.Context, config string, settings protocol.GrandmasterSettings) error {
	if err := client.wait(ctx, "gm-set"); err != nil {
		return err
	}
	return client.MockClient.SetGMSettingsContext(ctx, config, settings)
}
func (client *managerPMCClient) GetParentTimeAndCurrentDSContext(ctx context.Context, config string) (pmc.ParentTimeCurrentDS, error) {
	if err := client.wait(ctx, "tbc-get"); err != nil {
		return pmc.ParentTimeCurrentDS{}, err
	}
	return client.MockClient.GetParentTimeAndCurrentDSContext(ctx, config)
}
func (client *managerPMCClient) SetExternalGMPropertiesNPContext(ctx context.Context, config string, properties protocol.ExternalGrandmasterProperties) error {
	if err := client.wait(ctx, "external-set"); err != nil {
		return err
	}
	return client.MockClient.SetExternalGMPropertiesNPContext(ctx, config, properties)
}

func managedTBCInputs(manager *ClockManager, config string) {
	manager.ProcessEvent(event.Event{Source: event.PMC, CfgName: config, Data: &event.ParentDSData{ParentDataSet: protocol.ParentDataSet{GrandmasterClockClass: 6}}})
	for index := 0; index <= event.WindowSize+1; index++ {
		manager.ProcessEvent(event.Event{Source: event.DPLL, ClockType: event.TBC, IFace: "ens1f0", CfgName: config, Time: time.Now().UnixMilli(), Data: &event.DPLLData{State: event.PTP_LOCKED, LeadingSource: true, Offset: utils.ToPtr[int64](10), InSyncConditionThreshold: 10000, InSyncConditionTimes: 1, ToFreeRunThreshold: 1500, MaxInSpecOffset: 500}})
		manager.ProcessEvent(event.Event{Source: event.PTP4l, ClockType: event.TBC, IFace: "ens1f0", CfgName: config, Time: time.Now().UnixMilli(), Data: &event.OffsetData{State: event.PTP_LOCKED, Offset: 10}})
	}
}

func TestManagerRemainsResponsiveWithEachOwnedPMCOperationBlocked(t *testing.T) {
	for _, kind := range []string{"gm-get", "gm-set", "tbc-get", "external-set"} {
		for _, retire := range []string{"reset", "remove", "replace", "shutdown"} {
			t.Run(kind+"/"+retire, func(t *testing.T) {
				client := &managerPMCClient{blocked: kind, entered: make(chan context.Context, 1)}
				client.ParentTimeCurrentDSResult = pmc.ParentTimeCurrentDS{ParentDataSet: protocol.ParentDataSet{GrandmasterClockClass: 6}}
				channel := make(chan event.Event, 100)
				metric := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "managed_pmc_progress_offset"}, []string{fromLabel, processLabel, nodeLabel, ifaceLabel})
				cache := ipc.NewCache(100)
				manager := Init("managed-pmc", channel, metric, nil, nil, cache)
				unrelated, err := clock.NewBC("unrelated", false, event.PtpClockThreshold{MaxOffsetThreshold: 100, MinOffsetThreshold: -100})
				require.NoError(t, err)
				require.NoError(t, manager.AddClock(unrelated))
				config := "ptp4l.managed.config"
				var owned clock.Clock
				if kind == "gm-get" || kind == "gm-set" {
					owned, err = clock.NewGM(config, func() int { return 37 }, client)
				} else {
					owned, err = clock.NewTBC(config, func() int { return 37 }, client, "ptp4l.controlled.config")
				}
				require.NoError(t, err)
				require.NoError(t, manager.AddClock(owned))
				ctx, cancel := context.WithCancel(context.Background())
				finished := make(chan struct{})
				go func() { manager.ProcessEvents(ctx); close(finished) }()
				t.Cleanup(func() { cancel(); <-finished })
				if kind == "gm-get" || kind == "gm-set" {
					manager.ProcessEvent(event.Event{Source: event.GNSS, IFace: "ens1f0", ClockType: event.GM, CfgName: config, Data: &event.GNSSData{GPSStatus: 0}})
				} else {
					managedTBCInputs(manager, config)
					if kind == "external-set" {
						manager.ProcessEvent(event.Event{Source: event.DPLL, ClockType: event.TBC, IFace: "ens1f0", CfgName: config, Time: time.Now().UnixMilli(), Data: &event.DPLLData{State: event.PTP_FREERUN, Offset: utils.ToPtr[int64](2000)}})
					}
				}
				var active context.Context
				select {
				case active = <-client.entered:
				case <-time.After(5 * time.Second):
					t.Fatal("owned operation never started")
				}
				progress := make(chan struct{})
				go func() {
					manager.ProcessEvent(event.Event{Source: event.PTP4l, IFace: "unrelated-port", CfgName: "unrelated", Time: time.Now().UnixMilli(), Data: &event.OffsetData{State: event.PTP_LOCKED, Offset: 17}})
					close(progress)
				}()
				select {
				case <-progress:
				case <-time.After(5 * time.Second):
					t.Fatal("PMC blocked unrelated event processing")
				}
				require.Equal(t, float64(17), testutil.ToFloat64(metric.With(prometheus.Labels{fromLabel: string(event.PTP4l), processLabel: string(event.PTP4l), nodeLabel: "managed-pmc", ifaceLabel: alias.GetAlias("unrelated-port")})))
				foundNotification := false
				for _, message := range cache.Snapshot() {
					if message.Profile == "unrelated" && message.Type == ipc.TypePTPState {
						foundNotification = true
					}
				}
				require.True(t, foundNotification, "unrelated IPC state notification must progress while PMC is blocked")
				switch retire {
				case "reset":
					manager.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: config, Reset: true})
				case "remove":
					manager.RemoveAllClocks()
				case "replace":
					replacement, createErr := clock.NewGM(config, func() int { return 37 }, &pmc.MockClient{})
					require.NoError(t, createErr)
					require.NoError(t, manager.AddClock(replacement))
				case "shutdown":
					cancel()
					<-finished
				}
				select {
				case <-active.Done():
				case <-time.After(5 * time.Second):
					t.Fatal("lifecycle transition failed to cancel PMC")
				}
			})
		}
	}
}

type managedAliasClock struct {
	clock.Clock
	calls       atomic.Int32
	done        chan struct{}
	stopStarted chan struct{}
}

func (c *managedAliasClock) StopManagedWorkers() <-chan struct{} {
	c.calls.Add(1)
	select {
	case c.stopStarted <- struct{}{}:
	default:
	}
	return c.done
}

func TestManagerRetiresAliasOwnerOnceAndJoinsOutsideEventLock(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(fmt.Sprint(replace), func(t *testing.T) { exerciseManagedAliasRetirement(t, replace) })
	}
}

func exerciseManagedAliasRetirement(t *testing.T, replace bool) {
	t.Helper()
	manager := Init("managed-alias", make(chan event.Event), nil, nil, nil, nil)
	base, err := clock.NewGM("ptp4l.alias.config", func() int { return 37 }, &pmc.MockClient{})
	require.NoError(t, err)
	old := &managedAliasClock{Clock: base, done: make(chan struct{}), stopStarted: make(chan struct{}, 1)}
	require.NoError(t, manager.AddClock(old))
	replacement, err := clock.NewGM(base.ConfigName(), func() int { return 37 }, &pmc.MockClient{})
	require.NoError(t, err)
	var release sync.Once
	releaseOwner := func() { release.Do(func() { close(old.done) }) }
	finished := make(chan struct{})
	var replaceError error
	go func() {
		if replace {
			replaceError = manager.AddClock(replacement)
		} else {
			manager.RemoveAllClocks()
		}
		close(finished)
	}()
	t.Cleanup(func() { releaseOwner(); <-finished; <-replacement.StopManagedWorkers() })
	select {
	case <-old.stopStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("retirement did not cancel")
	}
	progress := make(chan struct{})
	go func() {
		manager.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: "other", Data: &event.StateData{State: event.PTP_FREERUN}})
		close(progress)
	}()
	select {
	case <-progress:
	case <-time.After(5 * time.Second):
		t.Fatal("join holds event lock")
	}
	releaseOwner()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("retirement did not join")
	}
	require.Equal(t, int32(1), old.calls.Load())
	require.NoError(t, replaceError)
	for _, alias := range base.ConfigNames() {
		if replace {
			require.Same(t, replacement, manager.GetClock(alias))
		} else {
			require.Nil(t, manager.GetClock(alias))
		}
	}
}
