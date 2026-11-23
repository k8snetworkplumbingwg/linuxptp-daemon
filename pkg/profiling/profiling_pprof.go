//go:build pprof

// Package profiling exposes Go pprof profiling endpoints.
package profiling

import (
	"net/http"
	"net/http/pprof"
	"os"
	"time"

	"github.com/golang/glog"
)

// Start launches a pprof HTTP server in a background goroutine, listening on defaultAddr.
func Start(defaultAddr string) {
	addr := os.Getenv("PPROF_ADDR")
	if addr == "" {
		addr = defaultAddr
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		glog.Warningf("pprof profiling server starting on %s (pprof debug build — do not use in production)", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			glog.Errorf("pprof profiling server failed: %v", err)
		}
	}()
}
