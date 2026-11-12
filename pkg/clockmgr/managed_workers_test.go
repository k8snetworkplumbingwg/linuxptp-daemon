package clockmgr

import (
	"context"
	"sync"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/stretchr/testify/require"
)

type managedTestClock struct {
	*clock.BCClock
	stopOnce sync.Once
	stopped  chan struct{}
	done     chan struct{}
}

func (c *managedTestClock) StopManagedWorkers() <-chan struct{} {
	c.stopOnce.Do(func() { close(c.stopped) })
	return c.done
}

func newManagedTestClock(t *testing.T) *managedTestClock {
	t.Helper()
	base, err := clock.NewBC(testClockmgrPtp4lConfig, false, event.PtpClockThreshold{})
	require.NoError(t, err)
	return &managedTestClock{BCClock: base, stopped: make(chan struct{}), done: make(chan struct{})}
}

func TestManagerManagedWorkerCapability(t *testing.T) {
	for _, action := range []string{"replace", "remove", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			owner := newManagedTestClock(t)
			manager := Init("node", make(chan event.Event, 8), nil, nil, nil, nil)
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(owner.done) }) }
			t.Cleanup(func() { release(); manager.RemoveAllClocks() })
			require.NoError(t, manager.AddClock(owner))
			manager.ProcessEvent(event.Event{CfgName: owner.ConfigName(), Reset: true})
			select {
			case <-owner.stopped:
				t.Fatal("Reset must not stop managed workers")
			default:
			}
			replacement, err := clock.NewBC(owner.ConfigName(), false, event.PtpClockThreshold{})
			require.NoError(t, err)
			finished := make(chan struct{})
			switch action {
			case "replace":
				go func() { defer close(finished); _ = manager.AddClock(replacement) }()
			case "remove":
				go func() { defer close(finished); manager.RemoveAllClocks() }()
			case "shutdown":
				ctx, cancel := context.WithCancel(context.Background())
				go func() { defer close(finished); manager.ProcessEvents(ctx) }()
				cancel()
			}
			awaitPMC(t, owner.stopped)
			responsive := make(chan struct{})
			go func() { manager.ProcessEvent(event.Event{CfgName: "unregistered"}); close(responsive) }()
			awaitPMC(t, responsive)
			manager.clockManagementMu.Lock()
			registered := manager.GetClock(owner.ConfigName())
			manager.clockManagementMu.Unlock()
			if action != "shutdown" {
				require.Nil(t, registered, "old owner must detach before join")
			}
			if manager.clockLifecycleMu.TryLock() {
				manager.clockLifecycleMu.Unlock()
				t.Fatal("retirement must retain lifecycle serialization while joining")
			}
			select {
			case <-finished:
				t.Fatal("managed completion must precede lifecycle return")
			default:
			}
			release()
			awaitPMC(t, finished)
			if action == "replace" {
				require.Same(t, replacement, manager.GetClock(owner.ConfigName()))
			}
		})
	}
}

func TestManagerSerializesReplacementAndRemoval(t *testing.T) {
	owner := newManagedTestClock(t)
	manager := Init("node", make(chan event.Event, 8), nil, nil, nil, nil)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(owner.done) }) }
	t.Cleanup(func() { release(); manager.RemoveAllClocks() })
	require.NoError(t, manager.AddClock(owner))
	replacement, err := clock.NewBC(owner.ConfigName(), false, event.PtpClockThreshold{})
	require.NoError(t, err)
	added, removed := make(chan struct{}), make(chan struct{})
	go func() { defer close(added); _ = manager.AddClock(replacement) }()
	awaitPMC(t, owner.stopped)
	if manager.clockLifecycleMu.TryLock() {
		manager.clockLifecycleMu.Unlock()
		t.Fatal("replacement must serialize removal through managed completion")
	}
	go func() { defer close(removed); manager.RemoveAllClocks() }()
	release()
	awaitPMC(t, added)
	awaitPMC(t, removed)
	require.Nil(t, manager.GetClock(owner.ConfigName()))
	require.NoError(t, manager.AddClock(replacement), "clocks without the capability remain supported")
	manager.RemoveAllClocks()
}
