package daemon

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

const labelConfig = "config"

// TestProcessRestartCountPreservedAcrossTeardownReapply verifies the OCPBUGS-7811
// contract by exercising the production cleanup path: deleteProcessStatusMetrics
// must not reset process_restart_count for the same (process, node, config) identity.
func TestProcessRestartCountPreservedAcrossTeardownReapply(t *testing.T) {
	origNode := NodeName
	NodeName = "restart-count-lifecycle-node"
	defer func() {
		labels := prometheus.Labels{
			labelProcess: "ts2phc",
			labelNode:    NodeName,
			labelConfig:  "ts2phc.0.config",
		}
		ProcessStatus.Delete(labels)
		ProcessRestartCount.Delete(labels) // test isolation only
		NodeName = origNode
	}()

	process := "ts2phc"
	cfg := "ts2phc.0.config"
	labels := prometheus.Labels{
		labelProcess: process,
		labelNode:    NodeName,
		labelConfig:  cfg,
	}

	// Isolate from any prior series with this identity.
	ProcessStatus.Delete(labels)
	ProcessRestartCount.Delete(labels)

	for i := 0; i < 9; i++ {
		UpdateProcessStatusMetrics(process, cfg, PtpProcessUp)
	}
	if got := testutil.ToFloat64(ProcessRestartCount.With(labels)); got != 9 {
		t.Fatalf("setup restart count: got %v want 9", got)
	}

	deleteProcessStatusMetrics(cfg, process)

	if got := testutil.ToFloat64(ProcessRestartCount.With(labels)); got != 9 {
		t.Fatalf("restart count changed during production teardown: got %v want 9", got)
	}

	// Re-apply UP (same path as process start after applyNodePTPProfiles).
	UpdateProcessStatusMetrics(process, cfg, PtpProcessUp)
	got := testutil.ToFloat64(ProcessRestartCount.With(labels))
	if got < 9 {
		t.Fatalf("restart count not monotonic after re-apply: got %v (initial 9)", got)
	}
	if got != 10 {
		t.Fatalf("after single UP Inc via UpdateProcessStatusMetrics: got %v want 10", got)
	}
}

// TestProcessRestartCountDeletedOnTeardown_legacyDocumentsOldBug keeps a
// regression note for the pre-fix Delete behavior (9 → absent → 5).
func TestProcessRestartCountDeletedOnTeardown_legacyDocumentsOldBug(t *testing.T) {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "openshift_ptp_process_restart_count_legacy",
	}, []string{labelProcess, labelNode, labelConfig})
	labels := prometheus.Labels{
		labelProcess: "ts2phc",
		labelNode:    "n1",
		labelConfig:  "ts2phc.0.config",
	}
	for i := 0; i < 9; i++ {
		c.With(labels).Inc()
	}
	c.Delete(labels) // old bug path
	if n := testutil.CollectAndCount(c); n != 0 {
		t.Fatalf("expected series absent after Delete, got %d", n)
	}
	for i := 0; i < 5; i++ {
		c.With(labels).Inc()
	}
	if got := testutil.ToFloat64(c.With(labels)); got != 5 {
		t.Fatalf("legacy Delete path should reset to 5; got %v", got)
	}
}
