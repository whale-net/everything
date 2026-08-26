package main

import (
	"fmt"
	"time"

	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// FR71/NFR5 granularity tier constants.
//
// Three tiers exist: raw, 5-minute and hourly (A14: no tier coarser than
// hourly in V1). Retention differs from serving limits, deliberately:
//
//   - Raw is *retained* at least 13 months (protects postmortems older than
//     a quarter, and gives FR20's boundary captures room to complete before
//     their chunk's raw is dropped), but *served* only for windows no longer
//     than 48 hours (NFR3.2's raw cap).
//   - The 5-minute tier is retained, and thus servable, for 90 days.
//   - The hourly tier is retained, and servable, indefinitely.
const (
	// RawServingWindowCap is NFR3.2's 48-hour cap on raw-row responses:
	// a window longer than this never returns raw, regardless of recency.
	RawServingWindowCap = 48 * time.Hour

	// FiveMinuteRetention is the 5-minute tier's retention floor (A12). A
	// window reaching further back than this can never resolve to the
	// 5-minute tier.
	FiveMinuteRetention = 90 * 24 * time.Hour

	// RawRetentionFloor is the raw tier's minimum retention (A12). Beyond
	// this, raw rows are not guaranteed to exist at all, independent of
	// NFR3.2's serving cap.
	RawRetentionFloor = 13 * 30 * 24 * time.Hour // 13 months, 30-day months
)

// TierResolution is the outcome of resolving a requested granularity against
// a window: the tier that will actually answer, and a human-readable reason
// stating why -- so that coarsening is disclosed, never silent (FR71). The
// reason is populated even when the requested tier was honored, so callers
// never have to infer "no coarsening happened" from an empty field.
type TierResolution struct {
	Tier   pb.GranularityTier
	Reason string
}

// ResolveTier decides which granularity tier answers a request for
// `requested` granularity over the window [windowStart, windowEnd), evaluated
// relative to `now`.
//
// FR71: a requested tier is a hint, not a contract. The server coarsens --
// and must -- wherever the requested tier's retention, or NFR3.2's 48-hour
// raw cap, cannot serve the window. It never serves a *finer* tier than
// requested: TierRaw and hourly-requested-but-servable-at-raw-precision are
// not equivalent, and a caller asking for the coarser tier gets it even when
// a finer one happens to be available.
//
// No tier coarser than hourly exists (A14): a request coarser than hourly
// (or GRANULARITY_TIER_UNSPECIFIED) is treated as a request for hourly, the
// coarsest tier available.
func ResolveTier(requested pb.GranularityTier, windowStart, windowEnd, now time.Time) (TierResolution, error) {
	if windowEnd.Before(windowStart) {
		return TierResolution{}, fmt.Errorf("resolve tier: window end %v is before window start %v", windowEnd, windowStart)
	}

	duration := windowEnd.Sub(windowStart)
	age := now.Sub(windowStart) // how far back the oldest requested instant reaches

	withinRawCap := duration <= RawServingWindowCap && age <= RawRetentionFloor
	withinFiveMinuteRetention := age <= FiveMinuteRetention

	switch requested {
	case pb.GranularityTier_GRANULARITY_TIER_RAW:
		if withinRawCap {
			return TierResolution{
				Tier:   pb.GranularityTier_GRANULARITY_TIER_RAW,
				Reason: "raw: window is within the 48-hour raw serving cap",
			}, nil
		}
		if withinFiveMinuteRetention {
			return TierResolution{
				Tier: pb.GranularityTier_GRANULARITY_TIER_5_MINUTE,
				Reason: fmt.Sprintf(
					"coarsened raw to 5-minute: requested window spans %s and/or reaches %s back, past the 48-hour raw serving cap",
					duration.Round(time.Minute), age.Round(time.Minute),
				),
			}, nil
		}
		return TierResolution{
			Tier: pb.GranularityTier_GRANULARITY_TIER_HOURLY,
			Reason: fmt.Sprintf(
				"coarsened raw to hourly: requested window reaches %s back, past both the 48-hour raw cap and the 90-day 5-minute retention floor",
				age.Round(time.Hour),
			),
		}, nil

	case pb.GranularityTier_GRANULARITY_TIER_5_MINUTE:
		if withinFiveMinuteRetention {
			return TierResolution{
				Tier:   pb.GranularityTier_GRANULARITY_TIER_5_MINUTE,
				Reason: "5-minute: window is within the 90-day 5-minute retention floor",
			}, nil
		}
		return TierResolution{
			Tier: pb.GranularityTier_GRANULARITY_TIER_HOURLY,
			Reason: fmt.Sprintf(
				"coarsened 5-minute to hourly: requested window reaches %s back, past the 90-day 5-minute retention floor",
				age.Round(time.Hour),
			),
		}, nil

	default:
		// GRANULARITY_TIER_HOURLY, GRANULARITY_TIER_UNSPECIFIED, and any
		// tier coarser than hourly (none exist in V1, A14) all resolve here.
		return TierResolution{
			Tier:   pb.GranularityTier_GRANULARITY_TIER_HOURLY,
			Reason: "hourly: retained indefinitely, the coarsest tier in V1",
		}, nil
	}
}
