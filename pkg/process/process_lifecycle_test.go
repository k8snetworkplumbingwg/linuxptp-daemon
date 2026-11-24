package process

import (
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"testing"
	"time"
)

func waitProcessState(t *testing.T, p Process, want State) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if p.State() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for state %v, got %v", want, p.State())
}

func waitProcessStatus(t *testing.T, ch <-chan event.Event, want int64) {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ps, ok := ev.Data.(*event.ProcessStatusData); ok && ps.Status == want {
				return
			}
		case <-deadline:
			t.Fatalf("timeout waiting for PROCESS_STATUS %d", want)
		}
	}
}
