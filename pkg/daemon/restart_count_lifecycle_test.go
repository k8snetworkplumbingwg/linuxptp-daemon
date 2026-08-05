package daemon

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

const (
	restartCountMetric  = "openshift_ptp_process_restart_count"
	processStatusMetric = "openshift_ptp_process_status"
	labelConfig         = "config"
)

// TestProcessRestartCountPreservedAcrossTeardownReapply verifies the OCPBUGS-7811
// contract: tearing down process_status must not reset process_restart_count for
// the same (process, node, config) identity.
func TestProcessRestartCountPreservedAcrossTeardownReapply(t *testing.T) {
	reg := prometheus.NewRegistry()
	labelNames := []string{labelProcess, labelNode, labelConfig}
	status := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: processStatusMetric,
	}, labelNames)
	restarts := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: restartCountMetric,
	}, labelNames)
	if err := reg.Register(status); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(restarts); err != nil {
		t.Fatal(err)
	}
	labels := prometheus.Labels{
		labelProcess: "ts2phc",
		labelNode:    "n1",
		labelConfig:  "ts2phc.0.config",
	}

	status.With(labels).Set(1)
	for i := 0; i < 9; i++ {
		restarts.With(labels).Inc()
	}
	if got := testutil.ToFloat64(restarts.With(labels)); got != 9 {
		t.Fatalf("setup restart count: got %v want 9", got)
	}

	// Fixed teardown: delete status only (mirrors deleteProcessStatusMetrics).
	status.Delete(labels)

	if n := testutil.CollectAndCount(status); n != 0 {
		t.Fatalf("process_status should be absent after teardown, got %d series", n)
	}
	if n := testutil.CollectAndCount(restarts); n != 1 {
		t.Fatalf("process_restart_count must remain after teardown (OCPBUGS-7811), got %d series", n)
	}
	if got := testutil.ToFloat64(restarts.With(labels)); got != 9 {
		t.Fatalf("restart count changed during teardown: got %v want 9", got)
	}

	// Re-apply: status UP + one restart Inc (same as UpdateProcessStatusMetrics).
	status.With(labels).Set(1)
	restarts.With(labels).Inc()
	got := testutil.ToFloat64(restarts.With(labels))
	if got < 9 {
		t.Fatalf("restart count not monotonic after re-apply: got %v (initial 9)", got)
	}
	if got != 10 {
		t.Fatalf("after single UP Inc: got %v want 10", got)
	}
	t.Logf("post-fix: status cleaned up, restart_count monotonic 9 → 10")
}

// TestProcessRestartCountDeletedOnTeardown_legacyDocumentsOldBug keeps a
// regression note for the pre-fix Delete behavior (9 → absent → 5).
func TestProcessRestartCountDeletedOnTeardown_legacyDocumentsOldBug(t *testing.T) {
	reg := prometheus.NewRegistry()
	c := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: restartCountMetric,
	}, []string{labelProcess, labelNode, labelConfig})
	if err := reg.Register(c); err != nil {
		t.Fatal(err)
	}
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
	if got := testutil.ToFloat64(c.With(labels)); got >= 9 {
		t.Fatalf("legacy Delete path should reset; got %v", got)
	}
}
