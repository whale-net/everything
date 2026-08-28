package loadtest

import (
	"math"
	"sort"
	"time"
)

// Percentile returns the p-th percentile (0 < p <= 1, e.g. 0.95 for p95) of
// durations, measured server-side by whatever called this -- the one
// percentile-measurement routine this package's doc comment promises is
// shared across every gate that uses this harness (NFR3.3's household
// landing gate and the Phase 3 series gate alike), so no gate re-derives
// its own percentile arithmetic. durations is sorted in place; pass a copy
// if the caller still needs the original order preserved.
//
// Nearest-rank method: index = ceil(p * n) - 1, clamped into [0, n-1].
// Panics on an empty slice -- a percentile of zero samples is a caller bug,
// not a valid measurement.
func Percentile(durations []time.Duration, p float64) time.Duration {
	if len(durations) == 0 {
		panic("loadtest.Percentile: durations is empty")
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })

	idx := int(math.Ceil(p*float64(len(durations)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(durations) {
		idx = len(durations) - 1
	}
	return durations[idx]
}
