// Package suspect defines FR26.3's enumerable Check registry: the fixed,
// named set of reasons a reading (or a bucket derived from one) can be
// marked suspect on a readings response. "Every suspect marker is
// attributable to a named, enumerable check" -- a marker is always one or
// more Check values from this package, never a free-form string a caller
// has to guess the meaning of, and All() lets a caller or a test list
// every possible marker identifier without touching a database.
//
// This package is deliberately data-only at scaffold time: it defines the
// stable wire identifiers and the tally shape (Marker, Counts) every
// readings response carries, per FR26.1/FR26.3 and this task's Scaffold
// section ("an enumerable Check registry ... each has a stable identifier
// returned in the response"). The actual detection logic behind each
// check -- the out-of-range comparison, the sensor_reading.valid lookup,
// the sensor_region_history disagreement test, the FR21 migration-snap
// window test -- is this task's Implementation phase, wired into
// leaflab/api/readings alongside the query that produces each reading or
// bucket in the first place (a marker needs the same row the value came
// from, not a second pass over this package).
//
// FR26.2's compensating control is load-bearing here: retroactive
// re-stamping of a reading's stored region_id is permanently out of scope
// (not deferred) -- a Check only ever marks a response-side annotation, and
// no function in this package (or any caller of it) may write back to
// sensor_reading.
package suspect

// Check identifies one specific, enumerable reason a reading or a bucket
// derived from readings is marked suspect (FR26.3). The underlying string
// is the stable identifier carried on the wire (ReadingPoint.suspect_checks,
// CurrentValue.suspect_checks, PeriodSummary.suspect_checks in
// leaflab/api/proto/api.proto) -- renaming a constant's value is a wire
// contract change, not a refactor.
type Check string

const (
	// CheckOutOfRange marks a reading whose value falls outside the valid
	// range for its measurement type (FR26.1: "an out-of-range value is
	// presented as suspect rather than as fact").
	CheckOutOfRange Check = "out_of_range"

	// CheckPersistedInvalidFlag marks a reading whose sensor_reading.valid
	// column (migration 001, indexed by idx_sensor_reading_invalid) is
	// FALSE -- a validity judgment already made and persisted at write
	// time, surfaced here rather than silently dropped or presented as
	// fact.
	CheckPersistedInvalidFlag Check = "persisted_invalid_flag"

	// CheckStaleAttribution marks a reading whose stamped region_id
	// disagrees with sensor_region_history at the reading's own
	// recorded_at -- the pre-FR73 stale-attribution window (FR26.2).
	// Marked, never rewritten and never re-stamped: retroactive
	// re-stamping of sensor_reading.region_id is permanently out of scope
	// (see this package's doc comment).
	CheckStaleAttribution Check = "stale_attribution"

	// CheckMigrationSnapWindow marks a reading inside the hour bucket a
	// removed plant shares with its successor, per FR21's disclosed cost
	// (migration 017's snapped-to-hour plant_region_history backfill: a
	// plant removed mid-hour and whatever plant next occupies its region
	// share that hour's bucket).
	CheckMigrationSnapWindow Check = "migration_snap_window"
)

// All returns every enumerable Check, in a fixed, stable order -- the
// "enumerable" half of FR26.3: a caller (or a test asserting the registry
// is complete and stable) can list every possible marker identifier
// without a database.
func All() []Check {
	return []Check{
		CheckOutOfRange,
		CheckPersistedInvalidFlag,
		CheckStaleAttribution,
		CheckMigrationSnapWindow,
	}
}

// String returns c's stable wire identifier.
func (c Check) String() string {
	return string(c)
}

// Marker is the set of checks that marked one reading (or one bucket
// derived from readings) suspect. A reading with no marks is not suspect
// -- Checks is nil, never a slice containing an empty string -- which is
// what makes "invalid, missing and zero" three distinguishable outcomes
// (FR26.1): a Marker's absence of checks is a real, present, non-suspect
// value, not the same wire shape as a gap.
type Marker struct {
	Checks []Check
}

// Suspect reports whether m carries at least one check.
func (m Marker) Suspect() bool {
	return len(m.Checks) > 0
}

// Strings renders m's Checks as their wire-form []string, for the
// per-row/per-item suspect_checks field every readings response carries
// (api.proto's ReadingPoint, CurrentValue and PeriodSummary messages).
// Returns nil (an absent/empty repeated field on the wire), never an empty
// non-nil slice, when m is not suspect.
func (m Marker) Strings() []string {
	if len(m.Checks) == 0 {
		return nil
	}
	out := make([]string, len(m.Checks))
	for i, c := range m.Checks {
		out[i] = c.String()
	}
	return out
}

// Counts is the top-level marked_count/returned_count pair every readings
// response carries (FR26.3): "every readings response states how many of
// the returned readings are marked, so a marker that covers everything is
// visible as such rather than silently universal."
type Counts struct {
	Marked   int64
	Returned int64
}

// CountMarkers computes Counts over markers -- one Marker per item this
// response returns, in the same order. Response builders (Implementation
// phase, leaflab/api server.go) call this once per response rather than
// reimplementing the tally, so marked_count/returned_count are computed
// identically everywhere they appear.
func CountMarkers(markers []Marker) Counts {
	c := Counts{Returned: int64(len(markers))}
	for _, m := range markers {
		if m.Suspect() {
			c.Marked++
		}
	}
	return c
}
