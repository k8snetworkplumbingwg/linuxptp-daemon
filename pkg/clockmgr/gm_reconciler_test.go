package clockmgr

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/stretchr/testify/require"
)

type blockedGMClient struct {
	pmc.MockClient
	entered  chan struct{}
	canceled chan struct{}
	release  chan struct{}
	blockSet bool
}

func (c *blockedGMClient) GetGMSettingsContext(ctx context.Context, _ string) (protocol.GrandmasterSettings, error) {
	if c.blockSet {
		return protocol.GrandmasterSettings{}, nil
	}
	return protocol.GrandmasterSettings{}, c.block(ctx)
}

func (c *blockedGMClient) SetGMSettingsContext(ctx context.Context, _ string, _ protocol.GrandmasterSettings) error {
	return c.block(ctx)
}

func (c *blockedGMClient) block(ctx context.Context) error {
	c.entered <- struct{}{}
	<-ctx.Done()
	c.canceled <- struct{}{}
	<-c.release
	return ctx.Err()
}

func awaitGM(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("GM lifecycle or event processing blocked")
	}
}

func TestGMReconciliationDoesNotBlockManagerLifecycle(t *testing.T) {
	for _, action := range []string{"reset", "remove", "replace", "shutdown", "supersede"} {
		for _, blockSet := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/set=%t", action, blockSet), func(t *testing.T) {
				client := &blockedGMClient{entered: make(chan struct{}, 8), canceled: make(chan struct{}, 8), release: make(chan struct{})}
				client.blockSet = blockSet
				var releaseOnce sync.Once
				release := func() { releaseOnce.Do(func() { close(client.release) }) }
				cache := ipc.NewCache(64)
				manager := Init("node", make(chan event.Event, 64), nil, nil, nil, cache)
				t.Cleanup(func() { release(); manager.RemoveAllClocks() })
				gm, err := clock.NewGM("ts2phc.0.config", func() int { return 37 }, client)
				require.NoError(t, err)
				require.NoError(t, manager.AddClock(gm))
				base := event.Event{CfgName: "ts2phc.0.config", IFace: "ens2f0", ClockType: event.GM, Time: 1}
				base.Source, base.Data = event.GNSS, &event.GNSSData{GPSStatus: 3}
				gm.GetData(event.GNSS).AddEvent(base)
				gm.GetData(event.GNSS).UpdateState()
				base.Source, base.Data = event.TS2PHC, &event.OffsetData{State: event.PTP_LOCKED}
				gm.GetData(event.TS2PHC).AddEvent(base)
				gm.GetData(event.TS2PHC).UpdateState()
				base.Source, base.Data = event.DPLL, &event.DPLLData{State: event.PTP_LOCKED, LeadingSource: true}
				manager.ProcessEvent(base)
				awaitGM(t, client.entered)
				base.Time++
				if action == "supersede" {
					base.Data = &event.DPLLData{State: event.PTP_HOLDOVER, LeadingSource: true, FrequencyTraceable: true, ModeledHoldover: true}
				}
				processed := make(chan struct{})
				go func() { manager.ProcessEvent(base); close(processed) }()
				awaitGM(t, processed)
				wantState := event.PTP_LOCKED
				if action == "supersede" {
					wantState = event.PTP_HOLDOVER
				}
				require.Equal(t, wantState, gm.GetState())
				require.NotEmpty(t, cache.Out(), "IPC must progress before PMC completes")
				require.NotEmpty(t, manager.StatsUpdatesCh(), "stats must progress before PMC completes")
				done := make(chan struct{})
				switch action {
				case "supersede":
					close(done)
				case "reset":
					go func() { manager.ProcessEvent(event.Event{CfgName: base.CfgName, Reset: true}); close(done) }()
					awaitGM(t, done)
				case "remove":
					go func() { manager.RemoveAllClocks(); close(done) }()
				case "replace":
					replacement, createErr := clock.NewGM(base.CfgName, func() int { return 37 }, &pmc.MockClient{})
					require.NoError(t, createErr)
					go func() { _ = manager.AddClock(replacement); close(done) }()
				case "shutdown":
					ctx, cancel := context.WithCancel(context.Background())
					go func() { manager.ProcessEvents(ctx); close(done) }()
					cancel()
				}
				awaitGM(t, client.canceled)
				responsive := make(chan struct{})
				go func() { manager.ProcessEvent(event.Event{CfgName: "unregistered"}); close(responsive) }()
				awaitGM(t, responsive)
				if action != "reset" && action != "supersede" {
					select {
					case <-done:
						t.Fatal("lifecycle returned before transport cleanup")
					default:
					}
				}
				release()
				awaitGM(t, done)
			})
		}
	}
}
