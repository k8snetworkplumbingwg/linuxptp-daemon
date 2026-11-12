package clock

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/stretchr/testify/require"
	kclock "k8s.io/utils/clock"
)

type pmcTestTimer struct {
	owner   *pmcTestTime
	when    time.Time
	ch      chan time.Time
	stopped bool
}

type pmcTestTime struct {
	kclock.RealClock
	mu      sync.Mutex
	now     time.Time
	timers  []*pmcTestTimer
	created chan *pmcTestTimer
}

type pmcHarness struct {
	worker *pmcReconciler
	timer  *pmcTestTime
	events chan event.Event
}

func deliverTBCResult(c *TBC, message *event.ParentTimeCurrentDS) {
	worker := c.pmcWorker
	worker.mu.Lock()
	identity := event.PMCResult{Owner: worker.owner, Generation: worker.generation, Revision: worker.revision, Token: message.Generation, Config: c.cfgName, Kind: "tbc", Datasets: message.ParentTimeCurrentDS}
	worker.mu.Unlock()
	c.handlePMCResult(&identity)
}

func newPMCTestTime() *pmcTestTime {
	return &pmcTestTime{now: time.Unix(100, 0), created: make(chan *pmcTestTimer, 1024)}
}
func (c *pmcTestTime) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *pmcTestTime) NewTimer(delay time.Duration) kclock.Timer {
	c.mu.Lock()
	timer := &pmcTestTimer{owner: c, when: c.now.Add(delay), ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, timer)
	c.mu.Unlock()
	c.created <- timer
	return timer
}
func (c *pmcTestTime) step(delay time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(delay)
	for _, timer := range c.timers {
		if !timer.stopped && !timer.when.After(c.now) {
			timer.stopped = true
			timer.ch <- c.now
		}
	}
}
func (timer *pmcTestTimer) C() <-chan time.Time { return timer.ch }
func (timer *pmcTestTimer) Stop() bool {
	timer.owner.mu.Lock()
	defer timer.owner.mu.Unlock()
	was := timer.stopped
	timer.stopped = true
	return !was
}
func (timer *pmcTestTimer) Reset(delay time.Duration) bool {
	timer.owner.mu.Lock()
	defer timer.owner.mu.Unlock()
	was := timer.stopped
	timer.stopped = false
	timer.when = timer.owner.now.Add(delay)
	return !was
}

func installPMCHarness(t *testing.T, worker *pmcReconciler) *pmcHarness {
	t.Helper()
	harness := &pmcHarness{worker: worker, timer: newPMCTestTime(), events: make(chan event.Event, 10)}
	worker.clock = harness.timer
	worker.setSink(func(ctx context.Context, ev event.Event) error {
		select {
		case harness.events <- ev:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	t.Cleanup(func() { <-worker.stop() })
	return harness
}

func (h *pmcHarness) next(t *testing.T) event.Event {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case ev := <-h.events:
			return ev
		case timer := <-h.timer.created:
			h.timer.mu.Lock()
			delay := timer.when.Sub(h.timer.now)
			h.timer.mu.Unlock()
			h.timer.step(max(0, delay))
		case <-deadline.C:
			t.Fatal("PMC completion was not delivered")
			return event.Event{}
		}
	}
}

func (h *pmcHarness) flush(t *testing.T, handle func(event.Event) SyncState) {
	t.Helper()
	for {
		ev := h.next(t)
		handle(ev)
		result, ok := ev.Data.(*event.PMCResult)
		require.True(t, ok)
		if result.Kind == "applied" {
			return
		}
	}
}
