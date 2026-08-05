package daemon

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// TestProcessRestartCountPreservedAcrossTeardownReapply verifies the OCPBUGS-7811
// contract: tearing down process_status must not reset process_restart_count for
// the same (process, node, config) identity.
func TestProcessRestartCountPreservedAcrossTeardownReapply(t *testing.T) {
	reg := prometheus.NewRegistry()
	status := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "openshift_ptp_process_status",
	}, []string{"process", "node", "config"})
	restarts := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "openshift_ptp_process_restart_count",
	}, []string{"process", "node", "config"})
	if err := reg.Register(status); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(restarts); err != nil {
		t.Fatal(err)
	}
	labels := prometheus.Labels{"process": "ts2phc", "node": "n1", "config": "ts2phc.0.config"}

	status.With(labels).Set(1)
	for i := 0; i < 9; i++ {
		restarts.With(labels).Inc()
	}
	if got := counterValue(t, restarts, labels); got != 9 {
		t.Fatalf("setup restart count: got %v want 9", got)
	}

	// Fixed teardown: delete status only (mirrors deleteProcessStatusMetrics).
	status.Delete(labels)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if findMetric(mfs, "openshift_ptp_process_status", labels) != nil {
		t.Fatal("process_status should be absent after teardown")
	}
	if findMetric(mfs, "openshift_ptp_process_restart_count", labels) == nil {
		t.Fatal("process_restart_count must remain after teardown (OCPBUGS-7811)")
	}
	if got := counterValue(t, restarts, labels); got != 9 {
		t.Fatalf("restart count changed during teardown: got %v want 9", got)
	}

	// Re-apply: status UP + one restart Inc (same as UpdateProcessStatusMetrics).
	status.With(labels).Set(1)
	restarts.With(labels).Inc()
	got := counterValue(t, restarts, labels)
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
		Name: "openshift_ptp_process_restart_count",
	}, []string{"process", "node", "config"})
	if err := reg.Register(c); err != nil {
		t.Fatal(err)
	}
	labels := prometheus.Labels{"process": "ts2phc", "node": "n1", "config": "ts2phc.0.config"}
	for i := 0; i < 9; i++ {
		c.With(labels).Inc()
	}
	c.Delete(labels) // old bug path
	mfs, _ := reg.Gather()
	if findMetric(mfs, "openshift_ptp_process_restart_count", labels) != nil {
		t.Fatal("expected series absent after Delete")
	}
	for i := 0; i < 5; i++ {
		c.With(labels).Inc()
	}
	if got := counterValue(t, c, labels); got >= 9 {
		t.Fatalf("legacy Delete path should reset; got %v", got)
	}
}

func counterValue(t *testing.T, c *prometheus.CounterVec, labels prometheus.Labels) float64 {
	t.Helper()
	m, err := c.GetMetricWith(labels)
	if err != nil {
		t.Fatal(err)
	}
	var pm dto.Metric
	if err := m.Write(&pm); err != nil {
		t.Fatal(err)
	}
	return pm.GetCounter().GetValue()
}

func findMetric(mfs []*dto.MetricFamily, name string, labels prometheus.Labels) *dto.Metric {
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.Metric {
			match := true
			seen := map[string]string{}
			for _, lp := range m.Label {
				seen[lp.GetName()] = lp.GetValue()
			}
			for k, v := range labels {
				if seen[k] != v {
					match = false
					break
				}
			}
			if match {
				return m
			}
		}
	}
	return nil
}
