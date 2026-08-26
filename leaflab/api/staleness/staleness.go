// Package staleness computes the A23 "not reporting" threshold, the single
// place FR79 (fleet health listing), FR42.2 and FR62 all derive staleness
// classification from — see leaflab/DATA.md or leaflab/ARCHITECTURE.md for
// the recorded design decision (A23 unresolved-by-design architect note).
package staleness

import "time"

// A23's stated constants: 3x the board's longest configured poll interval,
// floored at 15 minutes. Global, not per-household.
const (
	DefaultMultiplier = 3
	DefaultFloor      = 15 * time.Minute
)

// Config holds the A23 threshold parameters. Both fields are configurable
// via LEAFLAB_STALENESS_MULTIPLIER and LEAFLAB_STALENESS_FLOOR_SECONDS (see
// leaflab/api/ENV.md) so the threshold can be tuned without a redeploy.
// A zero value behaves as NewConfig() (A23 defaults) — every field is
// defaulted defensively in Threshold, not just at construction, so a
// zero-value Config used directly is still correct.
type Config struct {
	Multiplier int
	Floor      time.Duration
}

// NewConfig returns a Config using A23's default constants.
func NewConfig() Config {
	return Config{Multiplier: DefaultMultiplier, Floor: DefaultFloor}
}

// Threshold computes the A23 not-reporting threshold for a board given its
// longest configured poll interval: 3x that interval, floored at 15 minutes.
//
// Architect note carried from #1197 (unresolved by design): this should
// eventually derive from the board's effective publish cadence, not a
// per-sensor interval the firmware does not yet honour —
// sensorboard_dynamic_main.cc:136 still publishes on a single compile-time
// SENSOR_POLL_INTERVAL_MS. When that changes, only this function's input
// derivation needs to change; every caller (FR79, FR42.2, FR62) is unaffected.
func (c Config) Threshold(longestPollInterval time.Duration) time.Duration {
	multiplier := c.Multiplier
	if multiplier <= 0 {
		multiplier = DefaultMultiplier
	}
	floor := c.Floor
	if floor <= 0 {
		floor = DefaultFloor
	}
	t := longestPollInterval * time.Duration(multiplier)
	if t < floor {
		return floor
	}
	return t
}

// IsStale reports whether a board last seen at lastSeenAt is "not reporting"
// per A23, given its longest configured poll interval and the current time.
func (c Config) IsStale(now, lastSeenAt time.Time, longestPollInterval time.Duration) bool {
	return now.Sub(lastSeenAt) > c.Threshold(longestPollInterval)
}
