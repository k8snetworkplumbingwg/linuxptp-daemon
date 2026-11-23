//go:build !pprof

// Package profiling exposes Go pprof profiling endpoints. This no-op implementation is compiled into normal
// (production/release) builds, so the pprof HTTP handlers are not present in the shipped binary. Build with
// `-tags pprof` to compile the real implementation in profiling_pprof.go.
package profiling

// Start is a no-op
func Start(_ string) {}
