package contract

import (
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/api/tiers"
)

// ToGranularity converts a leaflab/api/tiers.Tier to its wire enum (FR71's
// disclosure requirement -- every read-path response states which tier
// actually answered). An unrecognized tier converts to
// GRANULARITY_UNSPECIFIED rather than panicking; tiers.Select never
// produces one, but this keeps the conversion total.
func ToGranularity(t tiers.Tier) pb.Granularity {
	switch t {
	case tiers.TierRaw:
		return pb.Granularity_GRANULARITY_RAW
	case tiers.TierFiveMinute:
		return pb.Granularity_GRANULARITY_FIVE_MINUTE
	case tiers.TierHourly:
		return pb.Granularity_GRANULARITY_HOURLY
	default:
		return pb.Granularity_GRANULARITY_UNSPECIFIED
	}
}

// FromGranularity is ToGranularity's inverse for a caller-requested hint
// (FR71: "a hint, not a contract"). GRANULARITY_UNSPECIFIED and any
// unrecognized value default to tiers.TierRaw -- the finest tier -- so an
// unset hint asks for the most precise answer the window can bear, exactly
// what tiers.Select's "only ever coarsens" contract expects as its
// starting point.
func FromGranularity(g pb.Granularity) tiers.Tier {
	switch g {
	case pb.Granularity_GRANULARITY_FIVE_MINUTE:
		return tiers.TierFiveMinute
	case pb.Granularity_GRANULARITY_HOURLY:
		return tiers.TierHourly
	default:
		return tiers.TierRaw
	}
}
