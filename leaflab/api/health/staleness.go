// Package health implements A23's "not reporting" staleness rule -- the
// single authority every FR79 (this task's ListFleetHealth/
// ResolveToHousehold), FR62 (household landing classification, a later
// task) and FR42 (re-send availability, Phase 4) consumer must import and
// call, rather than re-deriving A23's arithmetic locally. Do not let a
// second copy of this rule exist anywhere else in this codebase.
package health

import "time"

const (
	// StalenessFloor is A23's floor: no board is considered "not
	// reporting" sooner than this, no matter how short its configured
	// poll interval. A flat 15-minute constant on its own was rejected by
	// A23 ("wrong at both ends of the configured range") -- this floor
	// only ever raises 3x a very short configured interval up to
	// something operationally sane; it never substitutes for the 3x
	// multiplier at the long end of the configured range.
	StalenessFloor = 15 * time.Minute

	// StalenessMultiplier is A23's multiplier, applied to a board's
	// longest configured poll interval.
	StalenessMultiplier = 3

	// DefaultPollInterval is substituted for a sensor whose configured
	// poll_interval_ms is 0 ("use device default" -- see
	// firmware/proto/config.proto's SensorConfig.poll_interval_ms doc
	// comment). It matches sensorboard_dynamic_main.cc's compile-time
	// SENSOR_POLL_INTERVAL_MS default (60000ms) -- see Threshold's doc
	// comment (the Architect standing note) for why a *configured* value,
	// defaulted to the firmware's own compiled-in default, is what this
	// file derives A23's threshold from.
	DefaultPollInterval = 60 * time.Second
)

// Threshold computes A23's "not reporting" staleness threshold for one
// board: 3x its longest configured poll interval, floored at
// StalenessFloor. This is computed globally (A23: "Global, not
// per-household") -- callers never scope this arithmetic to a household.
//
// Architect standing note (recorded here per the task's Validation
// criterion, and in leaflab/DATA.md): A23 asks for a threshold that
// "should derive from the effective publish cadence", but the firmware
// does not yet honor per-sensor poll intervals -- sensorboard_dynamic_main.cc:136
// still publishes every sensor together on one compile-time
// SENSOR_POLL_INTERVAL_MS, ignoring SensorConfig.poll_interval_ms entirely
// (see that file's "TODO: use ConfigApplier::PollIntervalMs() for
// per-sensor scheduling"). This implementation derives the threshold from
// the *configured* poll interval -- the value an operator pushed via
// PushDeviceConfig, read from the board's active accepted config -- not
// from observed publish timestamps, for two reasons:
//
//  1. A23's text reads "longest configured poll interval", which names the
//     config value, not an inferred one.
//  2. An observed-cadence derivation would, today, collapse to the same
//     fixed value for every board regardless of what was configured, since
//     the firmware ignores the configured value and publishes on one
//     global compile-time constant -- that would defeat the per-board
//     behavior A23 and this task's own test fixtures require (e.g. a board
//     with a 1-minute configured interval going stale at 15 minutes,
//     floored; a board with a 10-minute configured interval going stale at
//     30 minutes, not floored).
//
// When the firmware gap closes (per-sensor intervals actually honored),
// this function's output becomes accurate to the real publish cadence with
// no change here -- it already computes from the value that will then be
// true.
func Threshold(longestConfiguredPollInterval time.Duration) time.Duration {
	t := StalenessMultiplier * longestConfiguredPollInterval
	if t < StalenessFloor {
		return StalenessFloor
	}
	return t
}

// EffectivePollInterval maps a raw SensorConfig.poll_interval_ms value (as
// stored in a board's active device_config) to the duration Threshold
// should treat it as: 0 ("use device default", per that field's proto doc
// comment) becomes DefaultPollInterval; any other value is taken as-is.
func EffectivePollInterval(pollIntervalMs uint32) time.Duration {
	if pollIntervalMs == 0 {
		return DefaultPollInterval
	}
	return time.Duration(pollIntervalMs) * time.Millisecond
}

// IsStale reports whether lastSeenAt is stale under A23 ("not reporting")
// as of now, given the board's longest configured poll interval. This is
// the one function a present-instant staleness check should call --
// FR79's ListFleetHealth/ResolveToHousehold reporting_state, FR62's
// household landing classification and FR42's re-send availability all
// route through this file rather than re-deriving Threshold's arithmetic
// at their own call sites.
func IsStale(lastSeenAt, now time.Time, longestConfiguredPollInterval time.Duration) bool {
	return now.Sub(lastSeenAt) > Threshold(longestConfiguredPollInterval)
}
