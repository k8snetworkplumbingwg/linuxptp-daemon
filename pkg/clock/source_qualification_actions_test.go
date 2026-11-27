package clock

import (
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/stretchr/testify/require"
)

func actionDecision(generation uint64, qualified bool) event.SourceQualification {
	return event.SourceQualification{Generation: generation, QualificationResult: utils.QualificationResult{Qualified: qualified}}
}

func waitSourceInputActions(t *testing.T, actions *SourceInputActions) {
	t.Helper()
	if done := actions.Done(); done != nil {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("source input actions did not finish")
		}
	}
}

func TestSourceInputActionsAreOrdered(t *testing.T) {
	// Hold the first action so later transitions must wait and retain their order.
	started := make(chan bool, 3)
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	first := true
	var actions SourceInputActions
	actions.reset(7, true)
	require.Equal(t, 100, cap(actions.pendingActions))
	require.NoError(t, actions.configure(7, "cfg", "profile", func(qualified bool) error {
		started <- qualified
		if first {
			first = false
			<-release
		}
		return nil
	}))
	t.Cleanup(func() { finish(); actions.Close() })
	// The first false is a loss action, not an unchanged zero-value state.
	actions.observe(actionDecision(7, false))
	select {
	case qualified := <-started:
		require.False(t, qualified)
	case <-time.After(time.Second):
		t.Fatal("first input action did not start")
	}
	actions.observe(actionDecision(7, true))
	actions.observe(actionDecision(7, false))
	require.Empty(t, started)
	finish()
	for _, want := range []bool{true, false} {
		select {
		case qualified := <-started:
			require.Equal(t, want, qualified)
		case <-time.After(time.Second):
			t.Fatal("pending input action did not run")
		}
	}
	waitSourceInputActions(t, &actions)
	// A new transition must restart the worker after it drains the queue.
	actions.observe(actionDecision(7, true))
	select {
	case qualified := <-started:
		require.True(t, qualified, "the worker must restart after becoming idle")
	case <-time.After(time.Second):
		t.Fatal("idle input worker did not restart")
	}
}

func TestSourceInputActionOverflowDropsAction(t *testing.T) {
	// Keep the worker busy while alternating transitions fill the bounded queue.
	started := make(chan bool, maxPendingSourceActions+1)
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	first := true
	var actions SourceInputActions
	actions.reset(7, true)
	require.NoError(t, actions.configure(7, "cfg", "profile", func(qualified bool) error {
		started <- qualified
		if first {
			first = false
			<-release
		}
		return nil
	}))
	t.Cleanup(func() { finish(); actions.Close() })
	actions.observe(actionDecision(7, false))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first input action did not start")
	}
	for i := 0; i < maxPendingSourceActions; i++ {
		actions.observe(actionDecision(7, i%2 == 0))
	}
	// The next transition is dropped, and repeating it must not retry the action.
	actions.observe(actionDecision(7, true))
	actions.observe(actionDecision(7, true))
	// The test and worker share the pending action queue.
	actions.mu.Lock()
	pending := len(actions.pendingActions)
	actions.mu.Unlock()
	require.Equal(t, maxPendingSourceActions, pending, "overflowed actions must not be retried")
	finish()
	for i := 0; i < maxPendingSourceActions; i++ {
		select {
		case qualified := <-started:
			require.Equal(t, i%2 == 0, qualified)
		case <-time.After(time.Second):
			t.Fatal("queued input action did not run")
		}
	}
	waitSourceInputActions(t, &actions)
	require.Empty(t, started)
}

func TestSourceInputActionShutdownPreservesPendingLoss(t *testing.T) {
	// Shutdown must drain queued losses but cancel any queued enable.
	started := make(chan bool, 3)
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	var actions SourceInputActions
	actions.reset(7, true)
	require.NoError(t, actions.configure(7, "cfg", "profile", func(qualified bool) error {
		started <- qualified
		if qualified {
			<-release
		}
		return nil
	}))
	t.Cleanup(func() { finish(); actions.Close() })
	actions.observe(actionDecision(7, true))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("input enable did not start")
	}
	actions.observe(actionDecision(7, false))
	actions.observe(actionDecision(7, true))
	actions.observe(actionDecision(7, false))
	closed := make(chan struct{})
	go func() { actions.Close(); close(closed) }()
	require.Eventually(t, func() bool {
		// The test and teardown goroutine share the closed flag.
		actions.mu.Lock()
		defer actions.mu.Unlock()
		return actions.closed
	}, time.Second, time.Millisecond)
	finish()
	for i := 0; i < 2; i++ {
		select {
		case qualified := <-started:
			require.False(t, qualified, "pending enables must be cancelled")
		case <-time.After(time.Second):
			t.Fatal("teardown discarded a pending loss")
		}
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("teardown did not join the input worker")
	}
}

func TestSourceInputActionsDiscardOldGeneration(t *testing.T) {
	// Reconfiguration discards queued old actions; new actions wait for the
	// old in-flight action to finish.
	started := make(chan bool, 3)
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	first := true
	var actions SourceInputActions
	actions.reset(7, true)
	require.NoError(t, actions.configure(7, "cfg", "profile", func(qualified bool) error {
		started <- qualified
		if first {
			first = false
			<-release
		}
		return nil
	}))
	t.Cleanup(func() { finish(); actions.Close() })
	actions.observe(actionDecision(7, false))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first input action did not start")
	}
	actions.observe(actionDecision(7, true))
	actions.reset(8, false)
	actions.observe(actionDecision(8, false))
	finish()
	select {
	case qualified := <-started:
		require.False(t, qualified, "an old generation must not enable the input")
	case <-time.After(time.Second):
		t.Fatal("new-generation action did not run")
	}
	waitSourceInputActions(t, &actions)
	require.Empty(t, started)
}

func TestOldSourceCannotCloseReplacementActions(t *testing.T) {
	// A retiring publisher must not close the worker's replacement generation.
	var actions SourceInputActions
	actions.reset(7, true)
	require.NoError(t, actions.configure(7, "cfg", "old", func(bool) error { return nil }))
	actions.reset(8, true)
	called := make(chan bool, 1)
	require.NoError(t, actions.configure(8, "cfg", "new", func(qualified bool) error { called <- qualified; return nil }))
	t.Cleanup(actions.Close)
	actions.CloseGeneration(7)
	actions.observe(actionDecision(8, true))
	select {
	case qualified := <-called:
		require.True(t, qualified)
	case <-time.After(time.Second):
		t.Fatal("old source closed the replacement input worker")
	}
}

func TestOldSourceTeardownWaitsOnlyForItsAction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Block both generations to distinguish waiting for the old action from
		// incorrectly waiting for the replacement action.
		oldStarted := make(chan struct{})
		oldRelease := make(chan struct{})
		newStarted := make(chan struct{})
		newRelease := make(chan struct{})
		var oldOnce sync.Once
		var newOnce sync.Once
		finishOld := func() { oldOnce.Do(func() { close(oldRelease) }) }
		finishNew := func() { newOnce.Do(func() { close(newRelease) }) }
		var actions SourceInputActions
		actions.reset(7, true)
		require.NoError(t, actions.configure(7, "cfg", "old", func(bool) error { close(oldStarted); <-oldRelease; return nil }))
		t.Cleanup(func() { finishOld(); finishNew(); actions.Close() })
		actions.observe(actionDecision(7, true))
		select {
		case <-oldStarted:
		case <-time.After(time.Second):
			t.Fatal("old input action did not start")
		}
		actions.reset(8, true)
		require.NoError(t, actions.configure(8, "cfg", "new", func(bool) error { close(newStarted); <-newRelease; return nil }))
		actions.observe(actionDecision(8, true))
		oldClosed := make(chan struct{})
		go func() { actions.CloseGeneration(7); close(oldClosed) }()
		// Ensure teardown has reached the old in-flight action before checking it.
		synctest.Wait()
		select {
		case <-oldClosed:
			t.Fatal("old source teardown did not wait for its in-flight action")
		default:
		}
		finishOld()
		select {
		case <-newStarted:
		case <-time.After(time.Second):
			t.Fatal("replacement input action did not start")
		}
		// The replacement is still blocked, but old-generation teardown must finish.
		select {
		case <-oldClosed:
		case <-time.After(time.Second):
			t.Fatal("old source teardown waited for the replacement action")
		}
	})
}
