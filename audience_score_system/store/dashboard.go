// dashboard.go covers the per-Channel recent-activity dashboard on
// /channels/{id} (issue #2038, FR23-FR26, capability C20 -- root plan
// #2027's UI-feedback batch). C20 is web-only, no MCP mirror planned (see
// #2038's issue body); NOTE the C20 number currently collides with a
// retired-C6 disposition note in
// audience_score_system/product/02-capability-map.md -- the capability-map
// amendment resolving that collision is explicitly out of scope for this
// plan (#2038's issue body says so explicitly); do not edit the capability
// map from this file's change.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrChannelActivityNotImplemented is returned by
// DashboardStore.ChannelActivity until Implementation wires in the real
// bounded-round-trip SQL -- same scaffold/feat split other store methods
// in this package have followed (e.g. SyncStore.ListPublishedWithMetrics,
// sync.go; CalibrationStore.MonthlyTrend, calibration.go).
var ErrChannelActivityNotImplemented = errors.New("store: channel activity dashboard not implemented")

// TrendDirection classifies FR24's outcome-count comparison against the
// immediately preceding period of the same length. Root plan #2027's open
// question 2: issue #1959 states no baseline for "trending better" --
// this is the accepted assumption (prior-period-of-same-length) pending a
// possible future amendment (e.g. a CalibrationStore-derived accuracy
// baseline instead of raw counts); a later change to that baseline should
// be an informed one, not a rediscovery of this comment.
type TrendDirection string

const (
	TrendBetter TrendDirection = "better"
	TrendSame   TrendDirection = "same"
	TrendWorse  TrendDirection = "worse"
)

// ActivityCounts is one window's worth of FR23's five entity-type counts,
// all scoped to a single Channel:
//
//   - ResearchNotes: research_note rows created in the window.
//   - Ideas: idea rows created in the window.
//   - Verdicts: viability_verdict rows created in the window (an
//     append-only log -- every version counts, not just current ones).
//   - VideoScripts: video_script rows created in the window.
//   - LinkedVideos: video_schedule_match rows FIRST RECORDED in the
//     window -- created_at, never updated_at/resolved_at. A match
//     recorded outside the window but resolved (confirmed/rejected)
//     inside it does NOT count here; a video published inside the window
//     with no match row does not count either (FR23's load-bearing
//     "newly-linked video" definition).
type ActivityCounts struct {
	ResearchNotes int
	Ideas         int
	Verdicts      int
	VideoScripts  int
	LinkedVideos  int
}

// OutcomeWindow is FR24's outcome count for one window -- published
// SyncedVideos (published_at IS NOT NULL) with at least one recorded
// VideoMetrics row, published_at falling in the window -- plus a
// comparison against PriorCount, the identical count for the immediately
// preceding period of the same length (e.g. Outcomes24h.PriorCount is the
// 24h window immediately before Outcomes24h.Count's window). Trend is
// TrendBetter when Count > PriorCount, TrendWorse when Count <
// PriorCount, TrendSame otherwise -- including the 0-vs-0 case, which is
// explicitly "same", never "better".
type OutcomeWindow struct {
	Count      int
	PriorCount int
	Trend      TrendDirection
}

// ChannelActivity is FR23/FR24's per-Channel dashboard: activity counts
// over the trailing 24h and 7d windows (Last7d is a superset of Last24h,
// "in the last N", not a disjoint bucket), and the outcome count + trend
// for each window.
type ChannelActivity struct {
	Last24h, Last7d         ActivityCounts
	Outcomes24h, Outcomes7d OutcomeWindow
}

// DashboardStore covers the #2038 (FR23-FR26, C20) recent-activity
// dashboard: everything /channels/{id}'s dashboard section needs, over
// research_note/idea/viability_verdict/video_script/video_schedule_match/
// synced_video/video_metrics (migrations 002/010), reduced to a bounded
// number of round trips (NFR3) regardless of how much a Channel has
// accumulated. Performs NO authorization itself -- store.CanRead is
// applied by the caller (handleChannelDetail), same as every other store
// interface in this package.
type DashboardStore interface {
	// ChannelActivity returns FR23/FR24's windowed counts and outcome
	// trend for channelID, computed against a single request-time now
	// (passed in, never NOW() re-evaluated per window/section -- so a
	// window's two boundaries are internally consistent and callers/tests
	// are deterministic).
	//
	// Boundary rule (FR23): a window's lower bound is INCLUSIVE -- an
	// entity created/recorded exactly at now minus the window length
	// counts as IN that window (created_at >= lower bound).
	//
	// FR26: a Channel with zero activity in a window returns that
	// window's counts as zero -- a real ChannelActivity value, never an
	// error -- callers render it, never omit the section or treat it as a
	// failure.
	//
	// NFR3: implemented as a small, fixed number of SQL round trips -- one
	// per activity-count entity type/section plus one for the outcomes
	// section, each covering ALL of that section's windows in a single
	// COUNT(*) FILTER (WHERE ...) pass (mirroring calibration.go's
	// MonthlyTrend and browse.go's PredictionVsOutcome) -- never a
	// Go-side loop calling VerdictStore.History/SyncStore.LatestMetricsFor
	// once per idea/video, and never a separate query per window when one
	// FILTER pass already covers every window a section needs.
	ChannelActivity(ctx context.Context, channelID uuid.UUID, now time.Time) (ChannelActivity, error)
}

// dashboardStore implements DashboardStore against `research_note`,
// `idea`, `viability_verdict`, `video_script`, `video_schedule_match`,
// `synced_video`, and `video_metrics` (migrations 002/010).
type dashboardStore struct{ pool *pgxpool.Pool }

var _ DashboardStore = dashboardStore{}

// ChannelActivity is stubbed for Scaffold -- this task's Implementation
// step replaces this body with the real bounded-round-trip SQL described
// on the DashboardStore.ChannelActivity doc comment above.
func (s dashboardStore) ChannelActivity(ctx context.Context, channelID uuid.UUID, now time.Time) (ChannelActivity, error) {
	return ChannelActivity{}, ErrChannelActivityNotImplemented
}
