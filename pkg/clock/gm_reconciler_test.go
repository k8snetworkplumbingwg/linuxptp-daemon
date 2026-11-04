package clock

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/stretchr/testify/require"
	kclock "k8s.io/utils/clock"
)

type gmTestTimer struct {
	owner   *gmTestTime
	when    time.Time
	ch      chan time.Time
	stopped bool
}

type gmTestTime struct {
	kclock.RealClock
	mu     sync.Mutex
	now    time.Time
	timers []*gmTestTimer
}

type gmRequest struct {
	ctx      context.Context
	settings *protocol.GrandmasterSettings
	reply    chan error
}

type gmScriptClient struct {
	requests chan gmRequest
	stop     chan struct{}
}

func newGMTestTime() *gmTestTime     { return &gmTestTime{now: time.Unix(100, 0)} }
func (c *gmTestTime) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *gmTestTime) NewTimer(delay time.Duration) kclock.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &gmTestTimer{owner: c, when: c.now.Add(delay), ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, timer)
	return timer
}
func (c *gmTestTime) step(delay time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(delay)
	for _, timer := range c.timers {
		if !timer.stopped && !timer.when.After(c.now) {
			timer.ch <- c.now
			timer.stopped = true
		}
	}
}
func (t *gmTestTimer) C() <-chan time.Time { return t.ch }
func (t *gmTestTimer) Stop() bool {
	t.owner.mu.Lock()
	defer t.owner.mu.Unlock()
	was := t.stopped
	t.stopped = true
	return !was
}
func (t *gmTestTimer) Reset(delay time.Duration) bool {
	t.owner.mu.Lock()
	defer t.owner.mu.Unlock()
	was := t.stopped
	t.when = t.owner.now.Add(delay)
	t.stopped = false
	return !was
}

func advanceGM(t *testing.T, r *gmReconciler) {
	t.Helper()
	r.mu.Lock()
	r.clock.(*gmTestTime).step(max(r.next.Sub(r.clock.Now()), 0) + time.Nanosecond)
	r.mu.Unlock()
}

func waitGM(t *testing.T, gm *GM) gmDesired {
	t.Helper()
	var result gmDesired
	require.Eventually(t, func() bool {
		gm.reconciler.mu.Lock()
		defer gm.reconciler.mu.Unlock()
		result = gm.reconciler.announced
		return gm.reconciler.haveAnnounced && result == gm.reconciler.desired && gm.reconciler.cancel == nil
	}, time.Second, time.Millisecond)
	return result
}

func (c *gmScriptClient) GetGMSettingsContext(ctx context.Context, _ string) (protocol.GrandmasterSettings, error) {
	request := gmRequest{ctx: ctx, reply: make(chan error, 1)}
	c.requests <- request
	select {
	case err := <-request.reply:
		return protocol.GrandmasterSettings{}, err
	case <-c.stop:
		return protocol.GrandmasterSettings{}, context.Canceled
	}
}
func (c *gmScriptClient) SetGMSettingsContext(ctx context.Context, _ string, settings protocol.GrandmasterSettings) error {
	request := gmRequest{ctx: ctx, settings: &settings, reply: make(chan error, 1)}
	c.requests <- request
	select {
	case err := <-request.reply:
		return err
	case <-c.stop:
		return context.Canceled
	}
}
func nextGMRequest(t *testing.T, client *gmScriptClient) gmRequest {
	t.Helper()
	select {
	case request := <-client.requests:
		return request
	case <-time.After(time.Second):
		t.Fatal("missing GM request")
		return gmRequest{}
	}
}

func TestGMReconcilerRejectsStaleGETSETAndReset(t *testing.T) {
	client := &gmScriptClient{requests: make(chan gmRequest, 8), stop: make(chan struct{})}
	r := newGMReconciler(client, "ptp4l.0.config", testTS2PHCCfg, newGMTestTime(), nil)
	t.Cleanup(func() { close(client.stop); <-r.reset(true) })
	desired := gmDesired{class: fbprotocol.ClockClass6, accuracy: fbprotocol.ClockAccuracyNanosecond100, utcOffset: 37}
	r.submit(desired)
	oldGet := nextGMRequest(t, client)
	desired.class = fbprotocol.ClockClass7
	r.submit(desired)
	require.ErrorIs(t, oldGet.ctx.Err(), context.Canceled)
	oldGet.reply <- nil
	advanceGM(t, r)
	get := nextGMRequest(t, client)
	require.Nil(t, get.settings, "stale GET must not issue a SET")
	get.reply <- nil
	oldSet := nextGMRequest(t, client)
	require.Equal(t, fbprotocol.ClockClass7, oldSet.settings.ClockQuality.ClockClass)
	desired.class = protocol.ClockClassOutOfSpec
	r.submit(desired)
	require.ErrorIs(t, oldSet.ctx.Err(), context.Canceled)
	oldSet.reply <- nil
	advanceGM(t, r)
	get = nextGMRequest(t, client)
	r.mu.Lock()
	haveAnnounced := r.haveAnnounced
	r.mu.Unlock()
	require.False(t, haveAnnounced, "stale SET must not acknowledge class 7")
	get.reply <- nil
	set := nextGMRequest(t, client)
	require.Equal(t, protocol.ClockClassOutOfSpec, set.settings.ClockQuality.ClockClass)
	r.reset(false)
	set.reply <- nil
	r.submit(desired)
	advanceGM(t, r)
	get = nextGMRequest(t, client)
	r.mu.Lock()
	haveAnnounced = r.haveAnnounced
	r.mu.Unlock()
	require.False(t, haveAnnounced, "old generation must not acknowledge an identical desired state")
	get.reply <- nil
	set = nextGMRequest(t, client)
	set.reply <- nil
	waitGM(t, &GM{reconciler: r})
	advanceGM(t, r)
	get = nextGMRequest(t, client)
	done := r.reset(true)
	get.reply <- nil
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not terminate")
	}
	require.Empty(t, client.requests)
}

func TestGMReconcilerBackoffCoalescesBursts(t *testing.T) {
	client := &gmScriptClient{requests: make(chan gmRequest, 8), stop: make(chan struct{})}
	r := newGMReconciler(client, "ptp4l.0.config", testTS2PHCCfg, newGMTestTime(), nil)
	t.Cleanup(func() { close(client.stop); <-r.reset(true) })
	desired := gmDesired{class: fbprotocol.ClockClass7, accuracy: fbprotocol.ClockAccuracyUnknown}
	r.submit(desired)
	for attempt := 0; attempt < 7; attempt++ {
		get := nextGMRequest(t, client)
		get.reply <- context.DeadlineExceeded
		require.Eventually(t, func() bool {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.cancel == nil
		}, time.Second, time.Millisecond)
		for sample := 0; sample < 100; sample++ {
			desired.utcOffset = int32(sample)
			r.submit(desired)
		}
		r.mu.Lock()
		backoff := r.backoff
		r.mu.Unlock()
		require.Equal(t, min(time.Second*time.Duration(1<<attempt), 30*time.Second), backoff)
		require.Empty(t, client.requests, "events must not bypass the timer")
		advanceGM(t, r)
	}
	get := nextGMRequest(t, client)
	get.reply <- nil
	set := nextGMRequest(t, client)
	require.Equal(t, int32(99), set.settings.TimePropertiesDS.CurrentUtcOffset, "only latest snapshot is applied")
	set.reply <- nil
	waitGM(t, &GM{reconciler: r})
}

func TestGMReconcilerDiscardsOldGenerationGETError(t *testing.T) {
	client := &gmScriptClient{requests: make(chan gmRequest, 8), stop: make(chan struct{})}
	r := newGMReconciler(client, "ptp4l.0.config", testTS2PHCCfg, newGMTestTime(), nil)
	t.Cleanup(func() { close(client.stop); <-r.reset(true) })
	desired := gmDesired{class: fbprotocol.ClockClass7, accuracy: fbprotocol.ClockAccuracyUnknown}
	r.submit(desired)
	get := nextGMRequest(t, client)
	r.reset(false)
	r.submit(desired)
	get.reply <- context.DeadlineExceeded
	advanceGM(t, r)
	get = nextGMRequest(t, client)
	r.mu.Lock()
	backoff, haveAnnounced := r.backoff, r.haveAnnounced
	r.mu.Unlock()
	require.Zero(t, backoff, "old errors must not delay a new episode")
	require.False(t, haveAnnounced)
	get.reply <- nil
	set := nextGMRequest(t, client)
	set.reply <- nil
	waitGM(t, &GM{reconciler: r})
}

func TestGMStoppedBeforeFirstEventCannotRestart(t *testing.T) {
	client := &pmc.MockClient{}
	gm, err := NewGM(testTS2PHCCfg, stubUtcOffset, client)
	require.NoError(t, err)
	gm.SetOsClock(&OsClock{})
	<-gm.CancelPMC()
	gm.Reset()
	gm.announceClockClassIfChanged(event.Event{}, SyncState{ClockClass: fbprotocol.ClockClass6})
	require.Nil(t, gm.reconciler)
	require.Empty(t, client.SnapshotGetCalls())
}

func TestGMPeriodicReconciliationRefreshesUTCWithoutEvents(t *testing.T) {
	client := &pmc.MockClient{}
	var utc atomic.Int32
	utc.Store(37)
	r := newGMReconciler(client, "ptp4l.0.config", testTS2PHCCfg, newGMTestTime(), func() int { return int(utc.Load()) })
	t.Cleanup(func() { <-r.reset(true) })
	r.submit(gmDesired{class: fbprotocol.ClockClass7, accuracy: fbprotocol.ClockAccuracyUnknown, utcOffset: 37})
	waitGM(t, &GM{reconciler: r})
	calls := client.SnapshotSetCalls()
	require.Len(t, calls, 1)
	client.SetGMSettingsResponse(*calls[0].GMSettings, nil, nil)
	utc.Store(38)
	advanceGM(t, r)
	require.Eventually(t, func() bool { return len(client.SnapshotSetCalls()) == 2 }, time.Second, time.Millisecond)
	require.Equal(t, int32(38), client.SnapshotSetCalls()[1].GMSettings.TimePropertiesDS.CurrentUtcOffset)
}

func TestGMLegacyAccuracySurvivesNonDPLLAndNilMeasurements(t *testing.T) {
	gm, _, _ := newTestGMClock(t)
	gm.syncState.ClockClass = fbprotocol.ClockClass7
	gm.announceClockClassIfChanged(event.Event{Source: event.DPLL, Data: &event.DPLLData{Offset: utils.ToPtr[int64](500)}}, gm.syncState)
	waitGM(t, gm)
	accuracy := fbprotocol.ClockAccuracyFromOffset(500 * time.Nanosecond)
	gm.syncState.ClockAccuracy = fbprotocol.ClockAccuracyNanosecond100
	gm.announceClockClassIfChanged(event.Event{Source: event.GNSS}, gm.syncState)
	require.Equal(t, accuracy, gm.syncState.ClockAccuracy)
	gm.announceClockClassIfChanged(event.Event{Source: event.DPLL, Data: &event.DPLLData{}}, gm.syncState)
	require.Equal(t, accuracy, gm.syncState.ClockAccuracy)
	gm.syncState.ClockClass = fbprotocol.ClockClass6
	gm.announceClockClassIfChanged(event.Event{}, gm.syncState)
	require.Equal(t, fbprotocol.ClockAccuracyNanosecond100, gm.syncState.ClockAccuracy)
}
