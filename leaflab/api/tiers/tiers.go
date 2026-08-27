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
// Scaffold only (this task's Scaffold phase, #1359): this task's
// Implementation phase fills in Select's actual coarsening logic --
// raw only within the 48-hour cap and within raw retention (13 months,
// migration 022), else 5-minute (90 days) or hourly (indefinite) -- and
// wires it into the read-path RPCs (a later phase task, per NFR8's fixed
// ordering). This package intentionally carries nothing about
// de-identification (NFR19): it names which pre-aggregated tier answered,
// not whether that answer is safe to publish -- any future k-suppression
// happens above tier selection, over contributors (FR71's "pre-aggregated
// is not de-identified" note), not here.
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

// ErrNotImplemented is returned by Select until this task's Implementation
// phase fills in the coarsening decision (FR71).
var ErrNotImplemented = errors.New("tiers: not implemented (Implementation phase, FR71)")

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

// Select picks the tier that can serve requested over [windowStart, windowEnd),
// coarsening away from requested when its retention or NFR3.2's 48-hour raw
// cap cannot serve the window -- never erroring and never silently
// returning raw instead.
func Select(requested Tier, windowStart, windowEnd time.Time) (Selection, error) {
	return Selection{}, ErrNotImplemented
}
