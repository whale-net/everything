// Package tiers implements FR71's granularity-tier selection: given a
// caller-requested granularity and a query window, pick the tier -- raw,
// sensor_reading_5m or sensor_reading_1h (migration 022) -- that can
// actually serve the window, and always report which tier answered.
//
// FR71: "A caller-requested granularity is a hint, not a contract: the
// server may coarsen -- and must, where the requested tier's retention or
// NFR3.2's 48-hour raw cap cannot serve the window. The response always
// states which tier answered, so coarsening is disclosed and never
// silent." Three tiers, no tier coarser than hourly in V1.
//
// Select implements the coarsening decision (this task's Implementation
// phase, #1359): raw only within the 48-hour cap and within raw retention
// (13 months, migration 022), else 5-minute (90 days) or hourly
// (indefinite). Wiring Select into the read-path RPCs is a later phase task
// (per NFR8's fixed ordering), not this package's concern. This package
// intentionally carries nothing about de-identification (NFR19): it names
// which pre-aggregated tier answered, not whether that answer is safe to
// publish -- any future k-suppression happens above tier selection, over
// contributors (FR71's "pre-aggregated is not de-identified" note), not
// here.
package tiers

import (
	"errors"
	"time"
)

// Tier identifies which materialized granularity answered a request.
// Values name the underlying relation directly (raw hypertable or one of
// migration 022's continuous aggregates) so a caller can trace a response
// back to its source without a lookup table.
type Tier string

const (
	// TierRaw serves sensor_reading directly -- exact, but only within
	// NFR3.2's 48-hour cap and within raw retention (13 months).
	TierRaw Tier = "raw"
	// TierFiveMinute serves sensor_reading_5m (migration 022) -- retained
	// 90 days.
	TierFiveMinute Tier = "five_minute"
	// TierHourly serves sensor_reading_1h (migration 022) -- retained
	// indefinitely; the coarsest tier in V1 (FR71: "No tier coarser than
	// hourly in V1"), and the tier an FR20 boundary partial inherits
	// retention from regardless of which tier it originally split.
	TierHourly Tier = "hourly"
)

// tierOrder lists the three tiers from finest to coarsest -- Select walks
// forward from the requested tier's position, never backward, so it only
// ever coarsens (FR71: "the server may coarsen").
var tierOrder = []Tier{TierRaw, TierFiveMinute, TierHourly}

// indexOf returns requested's position in tierOrder, or -1 if it is not one
// of the three known tiers.
func indexOf(t Tier) int {
	for i, candidate := range tierOrder {
		if candidate == t {
			return i
		}
	}
	return -1
}

// captureCompletionWindow mirrors migration 022's capture_completion_window
// base interval: FR20's boundary capture is "a deferred second write at
// bucket close," landing up to this long after the bucket it captures
// closes. Kept here only as a documentation cross-reference to the
// migration's derivation -- Select does not need it directly, since the raw
// cap (rawCapWindow, below) is always the binding constraint on raw well
// before the migration's derived 4-hour raw_retention_min floor would ever
// matter.
const captureCompletionWindow = time.Hour

// rawCapWindow is NFR3.2's 48-hour raw cap: raw cannot serve a window
// reaching further back than this, regardless of raw retention.
const rawCapWindow = 48 * time.Hour

// rawRetention mirrors migration 022's add_retention_policy(sensor_reading,
// drop_after => INTERVAL '13 months') (A12's business requirement). A month
// is approximated as 30 days for this time.Duration comparison -- rawCapWindow
// (48h) is the constraint that actually binds on raw in every normal
// request, so the approximation only matters for a pathological window
// whose start is many months in the past, which already fails rawCapWindow
// long before it would reach this boundary.
const rawRetention = 13 * 30 * 24 * time.Hour

// fiveMinuteRetention mirrors migration 022's add_retention_policy
// (sensor_reading_5m, drop_after => INTERVAL '90 days').
const fiveMinuteRetention = 90 * 24 * time.Hour

// CaptureCompletionWindow, RawRetention and FiveMinuteRetention re-export
// captureCompletionWindow, rawRetention and fiveMinuteRetention (above) for
// leaflab/api/capture. CaptureCompletionWindow and RawRetention implement
// FR20's NFR5 ordering check: a boundary_capture row still 'pending' as its
// raw chunk approaches rawRetention must fail loudly rather than silently
// losing the raw data its completion depends on. CaptureCompletionWindow is
// also the outside bound FR20's Implementation section gives the completer
// to finish a bucket after it closes -- the same margin migration 022's
// refresh-policy comment derives raw_retention_min from. FiveMinuteRetention
// is the differential-retention window migration 033's comment describes
// for boundary_partial: five_minute-tier partials are dropped once their
// bucket is this old, mirroring sensor_reading_5m's own retention, while
// hourly-tier partials -- the coarsest tier in V1 -- are never dropped
// (FR20.2: "retention on boundary_partial follows the coarsest tier the
// partial splits").
const (
	CaptureCompletionWindow = captureCompletionWindow
	RawRetention            = rawRetention
	FiveMinuteRetention     = fiveMinuteRetention
	// RawCapWindow re-exports rawCapWindow (above) for leaflab/api/readings'
	// FR30 config-lag check, which reuses NFR3.2's bounding to find a
	// board's most recent readings without scanning the hypertable
	// unbounded, rather than duplicating the 48-hour constant.
	RawCapWindow = rawCapWindow
)

// ErrUnknownTier is returned by Select when requested is not one of
// TierRaw, TierFiveMinute or TierHourly.
var ErrUnknownTier = errors.New("tiers: unrecognized requested tier")

// ErrInvalidWindow is returned by Select when windowEnd is before
// windowStart.
var ErrInvalidWindow = errors.New("tiers: windowEnd is before windowStart")

// Selection is the result of Select: which tier answered a request, always
// returned (FR71: "the response always states which tier answered, so
// coarsening is disclosed and never silent") regardless of whether the
// requested tier was actually served.
type Selection struct {
	// Tier is the tier that will actually answer the request -- may differ
	// from the caller-requested granularity (coarsened).
	Tier Tier
	// Coarsened is true when Tier is coarser than the caller's requested
	// granularity -- Select must set this whenever it coarsens, per FR71's
	// disclosure requirement.
	Coarsened bool
}

// Select picks the tier that can serve requested over [windowStart, windowEnd],
// coarsening away from requested when its retention or NFR3.2's 48-hour raw
// cap cannot serve the window. It only ever coarsens -- it never returns a
// tier finer than requested -- and it never silently substitutes raw or any
// other tier without saying so: Selection.Coarsened is set whenever the
// returned Tier differs from requested, satisfying FR71's "the response
// always states which tier answered" disclosure requirement.
//
// Tier capability is evaluated against how far windowStart reaches back from
// the current instant (time.Now()), which is what each tier's retention
// policy (migration 022) and NFR3.2's raw cap actually bound -- data older
// than a tier's retention has been dropped regardless of where windowEnd
// falls, and hourly (indefinite retention, migration 022 adds no retention
// policy for sensor_reading_1h) can always serve, so the walk below always
// terminates.
//
// Select returns an error only for invalid input (an unrecognized requested
// tier, or windowEnd before windowStart) -- never as a way of refusing a
// window it could instead coarsen to serve.
func Select(requested Tier, windowStart, windowEnd time.Time) (Selection, error) {
	if windowEnd.Before(windowStart) {
		return Selection{}, ErrInvalidWindow
	}
	startIdx := indexOf(requested)
	if startIdx == -1 {
		return Selection{}, ErrUnknownTier
	}

	now := time.Now()
	age := now.Sub(windowStart)

	for i := startIdx; i < len(tierOrder); i++ {
		if canServe(tierOrder[i], age) {
			return Selection{Tier: tierOrder[i], Coarsened: i != startIdx}, nil
		}
	}

	// Unreachable: TierHourly (the last entry in tierOrder) has no retention
	// bound and canServe always returns true for it. Kept as a defensive
	// fallback rather than a panic, so a future tierOrder edit that breaks
	// this invariant fails a test instead of crashing a caller.
	return Selection{Tier: TierHourly, Coarsened: TierHourly != requested}, nil
}

// canServe reports whether tier's retention (and, for raw, NFR3.2's 48-hour
// cap) reaches back far enough to still hold data as old as age.
func canServe(tier Tier, age time.Duration) bool {
	switch tier {
	case TierRaw:
		return age <= rawCapWindow && age <= rawRetention
	case TierFiveMinute:
		return age <= fiveMinuteRetention
	case TierHourly:
		return true
	default:
		return false
	}
}
