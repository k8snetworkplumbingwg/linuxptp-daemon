package clock

import (
	"context"
	"fmt"
	"sync"
	"time"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	kclock "k8s.io/utils/clock"
)

type gmDesired struct {
	class     fbprotocol.ClockClass
	accuracy  fbprotocol.ClockAccuracy
	utcOffset int32
}

type gmReconciler struct {
	mu            sync.Mutex
	client        pmc.GMSettingsClient
	utcOffset     func() int
	cfg           string
	logCfg        string
	clock         kclock.Clock
	wake          chan struct{}
	done          chan struct{}
	stopped       bool
	desired       gmDesired
	haveDesired   bool
	revision      uint64
	generation    uint64
	cancel        context.CancelFunc
	next          time.Time
	backoff       time.Duration
	announced     gmDesired
	haveAnnounced bool
}

func newGMReconciler(client pmc.GMSettingsClient, cfg, logCfg string, timerClock kclock.Clock, utcOffset func() int) *gmReconciler {
	r := &gmReconciler{client: client, utcOffset: utcOffset, cfg: cfg, logCfg: logCfg, clock: timerClock, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go r.run()
	return r
}

func (r *gmReconciler) signal() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *gmReconciler) submit(desired gmDesired) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped || (r.haveDesired && r.desired == desired) {
		return
	}
	r.revision++
	r.desired, r.haveDesired = desired, true
	if r.cancel != nil {
		r.cancel()
	}
	r.signal()
}

func (r *gmReconciler) reset(stop bool) <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.generation++
	r.haveDesired, r.haveAnnounced = false, false
	r.backoff = 0
	if deadline := r.clock.Now().Add(time.Second); r.next.After(deadline) {
		r.next = deadline
	}
	r.stopped = r.stopped || stop
	if r.cancel != nil {
		r.cancel()
	}
	r.signal()
	return r.done
}

func (r *gmReconciler) current(ctx context.Context, revision, generation uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return ctx.Err() == nil && !r.stopped && r.haveDesired && r.revision == revision && r.generation == generation
}

func (r *gmReconciler) reconcile(ctx context.Context, desired gmDesired, revision, generation uint64) error {
	actual, err := r.client.GetGMSettingsContext(ctx, r.cfg)
	if err != nil {
		return err
	}
	if !r.current(ctx, revision, generation) {
		return context.Canceled
	}
	if r.utcOffset != nil {
		desired.utcOffset = int32(r.utcOffset())
	}
	wanted := gmSettings(actual, desired)
	if actual != wanted {
		return r.client.SetGMSettingsContext(ctx, r.cfg, wanted)
	}
	return nil
}

func (r *gmReconciler) run() {
	defer close(r.done)
	for {
		r.mu.Lock()
		if r.stopped {
			r.mu.Unlock()
			return
		}
		if !r.haveDesired {
			r.mu.Unlock()
			<-r.wake
			continue
		}
		if delay := r.next.Sub(r.clock.Now()); delay > 0 {
			timer := r.clock.NewTimer(delay)
			r.mu.Unlock()
			select {
			case <-r.wake:
			case <-timer.C():
			}
			timer.Stop()
			continue
		}
		desired, revision, generation := r.desired, r.revision, r.generation
		ctx, cancel := context.WithCancel(context.Background())
		r.cancel = cancel
		r.next = r.clock.Now().Add(time.Second)
		r.mu.Unlock()
		err := r.reconcile(ctx, desired, revision, generation)
		r.mu.Lock()
		valid := ctx.Err() == nil && !r.stopped && r.haveDesired && revision == r.revision && generation == r.generation
		cancel()
		r.cancel = nil
		if valid {
			if err != nil {
				if r.backoff == 0 {
					r.backoff = time.Second
				} else {
					r.backoff = min(2*r.backoff, 30*time.Second)
				}
				r.next = r.clock.Now().Add(r.backoff)
				glog.Errorf("GM settings reconciliation failed for %s: %v", r.cfg, err)
			} else {
				if !r.haveAnnounced || r.announced.class != desired.class || r.announced.accuracy != desired.accuracy {
					fmt.Print(utils.GetClockClassLogMessage(event.PTP4lProcessName, r.logCfg, desired.class))
				}
				r.announced, r.haveAnnounced = desired, true
				r.backoff = 0
				r.next = r.clock.Now().Add(time.Second)
			}
		}
		r.mu.Unlock()
	}
}

func gmSettings(actual protocol.GrandmasterSettings, desired gmDesired) protocol.GrandmasterSettings {
	actual.ClockQuality.ClockClass = desired.class
	actual.ClockQuality.ClockAccuracy = desired.accuracy
	actual.TimePropertiesDS.PtpTimescale = true
	actual.TimePropertiesDS.CurrentUtcOffsetValid = true
	actual.TimePropertiesDS.CurrentUtcOffset = desired.utcOffset
	actual.TimePropertiesDS.FrequencyTraceable = true
	actual.TimePropertiesDS.TimeTraceable = desired.class == fbprotocol.ClockClass6 || desired.class == fbprotocol.ClockClass7
	actual.TimePropertiesDS.TimeSource = fbprotocol.TimeSourceInternalOscillator
	actual.ClockQuality.OffsetScaledLogVariance = 0xffff
	switch desired.class {
	case fbprotocol.ClockClass6:
		actual.ClockQuality.ClockAccuracy = fbprotocol.ClockAccuracyNanosecond100
		actual.TimePropertiesDS.TimeSource = fbprotocol.TimeSourceGNSS
		actual.ClockQuality.OffsetScaledLogVariance = 0x4e5d
	case protocol.ClockClassFreerun:
		actual.ClockQuality.ClockAccuracy = fbprotocol.ClockAccuracyUnknown
	}
	return actual
}
