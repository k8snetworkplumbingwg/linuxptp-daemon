package clock

import (
	"sync"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
)

// downstreamAnnouncement is a detached desired state, never live clock data.
type downstreamAnnouncement struct {
	client     pmc.Client
	config     string
	properties protocol.ExternalGrandmasterProperties
	settings   *protocol.GrandmasterSettings
	superseded <-chan struct{}
}

// downstreamAnnounceWriter serializes whole EGP/settings pairs off the clock
// loop. Only the latest pending state is needed; an in-flight PMC call always
// finishes before a newer pair starts. The worker exits when there is no work.
type downstreamAnnounceWriter struct {
	mu         sync.Mutex
	pending    *downstreamAnnouncement
	cancel     chan struct{}
	workerDone chan struct{} // nil when no worker is active
	closed     bool
}

func (w *downstreamAnnounceWriter) enqueue(client pmc.Client, config string, properties protocol.ExternalGrandmasterProperties, settings *protocol.GrandmasterSettings) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.invalidateLocked()
	w.cancel = make(chan struct{})
	update := &downstreamAnnouncement{client: client, config: config, properties: properties, superseded: w.cancel}
	if settings != nil {
		snapshot := *settings
		update.settings = &snapshot
	}
	w.pending = update
	if w.workerDone == nil {
		w.workerDone = make(chan struct{})
		go w.run()
	}
}

func (w *downstreamAnnounceWriter) run() {
	for {
		w.mu.Lock()
		update := w.pending
		w.pending = nil
		if update == nil {
			close(w.workerDone)
			w.workerDone = nil
			w.mu.Unlock()
			return
		}
		w.mu.Unlock()

		select {
		case <-update.superseded:
			continue
		default:
		}
		if err := update.client.SetExternalGMPropertiesNP(update.config, update.properties); err != nil {
			glog.Errorf("Failed to set external GM properties: %v", err)
		}
		if update.settings == nil {
			continue
		}
		select {
		case <-update.superseded:
			continue
		default:
		}
		if err := update.client.SetGMSettings(update.config, *update.settings); err != nil {
			glog.Errorf("Failed to set GM settings: %v", err)
		}
	}
}

func (w *downstreamAnnounceWriter) invalidateLocked() {
	if w.cancel != nil {
		close(w.cancel)
		w.cancel = nil
	}
	w.pending = nil
}

func (w *downstreamAnnounceWriter) invalidate() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.invalidateLocked()
}

func (w *downstreamAnnounceWriter) close() {
	w.mu.Lock()
	w.closed = true
	w.invalidateLocked()
	done := w.workerDone
	w.mu.Unlock()
	if done != nil {
		<-done
	}
}
