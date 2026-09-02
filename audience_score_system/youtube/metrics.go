package youtube

import (
	"context"
	"time"
)

// VideoMetrics is one Metrics result for a single video -- maps 1:1 onto
// store.VideoMetrics' data columns (migration 002, FR21) minus the
// SyncedVideoID foreign key, which only a caller holding the matching
// store.SyncedVideo row can resolve; YouTubeVideoID is the join key a
// caller (worker, #1574) uses to find it. Only views + retention +
// CTR/impressions are in M1 scope (C16 is out of scope), but the field set
// intentionally mirrors store.VideoMetrics so adding a metric later never
// requires changing this shape's meaning, just adding a field to both.
//
// Fields are pointers because YouTube Analytics can omit any of them (e.g.
// a just-published video with no rows yet) -- see Client.Metrics' doc
// comment on the zero-valued-row-not-error contract for that case.
type VideoMetrics struct {
	YouTubeVideoID             string
	Views                      *int64
	AverageViewDurationSeconds *float64
	AverageViewPercentage      *float64
	Impressions                *int64
	ImpressionCTR              *float64
	MeasuredAt                 time.Time
}

// Metrics returns views, retention, and CTR/impressions for each of
// videoIDs measured since since (FR21): retention from
// averageViewDuration/averageViewPercentage, CTR/impressions from
// impressions/impressionClickThroughRate, all via YouTube Analytics.
//
// Scaffold only (issue #1573): returns errNotImplemented. The real
// implementation must return a zero-valued VideoMetrics entry (not an
// error) for any single video with no Analytics rows yet -- an error here
// must only ever cover the whole request (e.g. auth/quota failure), never
// one video among many with partial data. Lands in the Implementation
// phase.
func (c *client) Metrics(ctx context.Context, channelID string, videoIDs []string, since time.Time) ([]VideoMetrics, error) {
	return nil, errNotImplemented
}
