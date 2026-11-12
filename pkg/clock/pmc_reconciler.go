package clock

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	kclock "k8s.io/utils/clock"
)

type pmcOperation struct {
	config     string
	external   bool
	settings   protocol.GrandmasterSettings
	properties protocol.ExternalGrandmasterProperties
}

type pmcWork struct {
	identity   event.PMCResult
	operations []pmcOperation
}

// pmcReconciler transports immutable plans; policy and result admission stay on
// the serialized clock loop. One latest slot per phase bounds event bursts.
type pmcReconciler struct {
	mu                          sync.Mutex
	client                      pmc.GMSettingsClient
	clock                       kclock.Clock
	sink                        func(context.Context, event.Event) error
	config                      string
	clockType                   event.ClockType
	owner, generation, revision uint64
	fetch, apply                *pmcWork
	cancel                      context.CancelFunc
	wake                        chan struct{}
	done                        chan struct{}
	started, stopped            bool
}

const pmcAppliedResult = "applied"

var pmcOwnerSequence atomic.Uint64

func newPMCReconciler(client pmc.GMSettingsClient, config string, clockType event.ClockType, timer kclock.Clock) *pmcReconciler {
	return &pmcReconciler{client: client, config: config, clockType: clockType, clock: timer,
		owner: pmcOwnerSequence.Add(1), wake: make(chan struct{}, 1), done: make(chan struct{})}
}

func (r *pmcReconciler) setSink(sink func(context.Context, event.Event) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sink = sink
}

func (r *pmcReconciler) signal() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *pmcReconciler) invalidate() {
	if r.cancel != nil {
		r.cancel()
	}
	r.generation++
	r.fetch, r.apply = nil, nil
}

func (r *pmcReconciler) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.invalidate()
	r.signal()
}

func (r *pmcReconciler) stop() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.stopped {
		r.stopped = true
		r.invalidate()
		if !r.started {
			close(r.done)
		}
		r.signal()
	}
	return r.done
}

func (r *pmcReconciler) current(identity event.PMCResult) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.matches(identity)
}

func (r *pmcReconciler) matches(identity event.PMCResult) bool {
	return !r.stopped && identity.Owner == r.owner && identity.Generation == r.generation && identity.Revision == r.revision
}

func (r *pmcReconciler) start() {
	if !r.started {
		r.started = true
		go r.run()
	}
	r.signal()
}

func (r *pmcReconciler) requestFetch(kind, config string, token uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return fmt.Errorf("PMC worker is stopped")
	}
	if r.sink == nil {
		return fmt.Errorf("PMC managed event sink is required for %s", r.config)
	}
	r.invalidate()
	r.revision++
	r.fetch = &pmcWork{identity: event.PMCResult{Owner: r.owner, Generation: r.generation, Revision: r.revision, Kind: kind, Config: config, Token: token}}
	r.start()
	return nil
}

// submit replaces the entire ordered plan. A fetch identity, when supplied,
// prevents a queued stale result from superseding a newer state transition.
func (r *pmcReconciler) submit(operations []pmcOperation, identity *event.PMCResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return fmt.Errorf("PMC worker is stopped")
	}
	if r.sink == nil {
		return fmt.Errorf("PMC managed event sink is required for %s", r.config)
	}
	if identity != nil && !r.matches(*identity) {
		return fmt.Errorf("stale PMC fetch result")
	}
	if identity == nil {
		r.invalidate()
		r.revision++
	} else if r.cancel != nil {
		r.cancel()
	}
	result := event.PMCResult{Owner: r.owner, Generation: r.generation, Revision: r.revision, Kind: pmcAppliedResult, Config: r.config}
	if identity != nil {
		result.Token = identity.Token
		result.Settings = identity.Settings
		result.Datasets = identity.Datasets
	}
	unique := make([]pmcOperation, 0, len(operations))
	for _, operation := range operations {
		if operation.config != "" && !slices.Contains(unique, operation) {
			unique = append(unique, operation)
		}
	}
	r.fetch = nil
	r.apply = &pmcWork{identity: result, operations: unique}
	r.start()
	return nil
}

func (r *pmcReconciler) take() (*pmcWork, context.Context, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return nil, nil, true
	}
	work := r.fetch
	if work == nil {
		work = r.apply
	}
	if work == nil {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	return work, ctx, false
}

func (r *pmcReconciler) run() {
	defer close(r.done)
	var next time.Time
	var lastStart time.Time
	var previous event.PMCResult
	backoff := time.Second
	for {
		work, ctx, stopped := r.take()
		if stopped {
			return
		}
		if work == nil {
			<-r.wake
			continue
		}
		if work.identity.Generation != previous.Generation || work.identity.Revision != previous.Revision {
			next = lastStart.Add(time.Second)
			backoff = time.Second
			previous = work.identity
		}
		if delay := next.Sub(r.clock.Now()); delay > 0 {
			timer := r.clock.NewTimer(delay)
			select {
			case <-timer.C():
			case <-ctx.Done():
			}
			timer.Stop()
			if ctx.Err() != nil {
				continue
			}
		}
		lastStart = r.clock.Now()
		next = lastStart.Add(time.Second)
		err := r.execute(ctx, work)
		canceled := ctx.Err() != nil
		r.mu.Lock()
		if r.cancel != nil && r.matches(work.identity) {
			r.cancel()
		}
		if r.matches(work.identity) {
			if err == nil {
				if r.fetch == work {
					r.fetch = nil
				}
				if r.apply == work {
					r.apply = nil
				}
				backoff = time.Second
			} else if !canceled {
				glog.Warningf("PMC reconcile %s revision %d: %v", r.config, work.identity.Revision, err)
				next = r.clock.Now().Add(backoff)
				backoff = min(30*time.Second, backoff*2)
			}
		}
		r.mu.Unlock()
	}
}

func (r *pmcReconciler) execute(ctx context.Context, work *pmcWork) error {
	result := work.identity
	var err error
	switch result.Kind {
	case "gm":
		result.Settings, err = r.client.GetGMSettingsContext(ctx, result.Config)
	case "tbc":
		client, ok := r.client.(pmc.DownstreamClient)
		if !ok {
			return fmt.Errorf("PMC downstream context capability is required")
		}
		result.Datasets, err = client.GetParentTimeAndCurrentDSContext(ctx, result.Config)
	case pmcAppliedResult:
		for _, operation := range work.operations {
			if ctx.Err() != nil || !r.current(result) {
				return context.Canceled
			}
			if operation.external {
				client, ok := r.client.(pmc.DownstreamClient)
				if !ok {
					return fmt.Errorf("PMC downstream context capability is required")
				}
				err = client.SetExternalGMPropertiesNPContext(ctx, operation.config, operation.properties)
			} else {
				err = r.client.SetGMSettingsContext(ctx, operation.config, operation.settings)
			}
			if err != nil {
				return err
			}
		}
	}
	if err != nil {
		return err
	}
	if ctx.Err() != nil || !r.current(result) {
		return context.Canceled
	}
	r.mu.Lock()
	sink := r.sink
	r.mu.Unlock()
	return sink(ctx, event.Event{Source: event.PMC, CfgName: r.config, ClockType: r.clockType, Time: r.clock.Now().UnixMilli(), Data: &result})
}
