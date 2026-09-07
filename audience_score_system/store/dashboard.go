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
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

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
	// NFR3: implemented as exactly two SQL round trips, fixed regardless
	// of how much a Channel has accumulated -- one query covering all
	// five FR23 entity-type sections (one CTE per entity type, each doing
	// a single COUNT(*) FILTER (WHERE ...) pass over both windows), and
	// one query covering FR24's outcomes section (all four counts --
	// both windows plus their prior periods -- via FILTER in a single
	// pass), mirroring calibration.go's MonthlyTrend and browse.go's
	// PredictionVsOutcome -- never a Go-side loop calling
	// VerdictStore.History/SyncStore.LatestMetricsFor once per idea/video,
	// and never a separate query per window or per entity type when one
	// FILTER pass already covers every window a section needs.
	ChannelActivity(ctx context.Context, channelID uuid.UUID, now time.Time) (ChannelActivity, error)
}

// dashboardStore implements DashboardStore against `research_note`,
// `idea`, `viability_verdict`, `video_script`, `video_schedule_match`,
// `synced_video`, and `video_metrics` (migrations 002/010).
type dashboardStore struct{ pool *pgxpool.Pool }

var _ DashboardStore = dashboardStore{}

// ChannelActivity implements DashboardStore.ChannelActivity in exactly two
// round trips regardless of how much a Channel has accumulated:
//
//  1. activityCountsQuery -- one CTE per FR23 entity type
//     (research_note/idea/viability_verdict/video_script/
//     video_schedule_match), each doing a single indexed scan bounded by
//     `created_at >= sevenDayLower` (the 7d window's lower bound, since
//     Last7d is a superset of Last24h) with a nested `COUNT(*) FILTER
//     (WHERE created_at >= oneDayLower)` teasing the 24h subset out of
//     that same scan -- never a second pass over the table for the second
//     window. The five CTEs are combined with a single trailing
//     CROSS JOIN (each produces exactly one row, so this is not a
//     cartesian blow-up).
//  2. outcomesQuery -- one pass over synced_video (FR24), FILTER-ing into
//     all four of Count24h/PriorCount24h/Count7d/PriorCount7d in the same
//     scan, an EXISTS subquery in place of a LATERAL join to video_metrics
//     since only presence of a metrics row (not its value) matters here.
//
// Both queries take `now` as a parameter (never NOW() re-evaluated per
// window) so a window's two boundaries -- and the two windows' shared
// upper bound of `now` -- are internally consistent.
func (s dashboardStore) ChannelActivity(ctx context.Context, channelID uuid.UUID, now time.Time) (ChannelActivity, error) {
	oneDayLower := now.Add(-24 * time.Hour)
	sevenDayLower := now.Add(-7 * 24 * time.Hour)

	var counts ChannelActivity
	err := s.pool.QueryRow(ctx, activityCountsQuery, channelID, sevenDayLower, oneDayLower).Scan(
		&counts.Last24h.ResearchNotes, &counts.Last7d.ResearchNotes,
		&counts.Last24h.Ideas, &counts.Last7d.Ideas,
		&counts.Last24h.Verdicts, &counts.Last7d.Verdicts,
		&counts.Last24h.VideoScripts, &counts.Last7d.VideoScripts,
		&counts.Last24h.LinkedVideos, &counts.Last7d.LinkedVideos,
	)
	if err != nil {
		return ChannelActivity{}, fmt.Errorf("query channel activity counts: %w", err)
	}

	// FR24's prior-period boundaries: the 24h window immediately preceding
	// [oneDayLower, now) is [now-48h, oneDayLower); the 7d window
	// immediately preceding [sevenDayLower, now) is [now-14d,
	// sevenDayLower). Root plan #2027's open question 2: #1959 states no
	// baseline for "trending better/worse" -- prior-period-of-same-length
	// is the accepted assumption this implements (see TrendDirection's
	// doc comment); a later change to the baseline (e.g. a
	// CalibrationStore-derived accuracy baseline instead of raw outcome
	// counts) should be an informed one, not a rediscovery of this
	// comment.
	priorOneDayLower := now.Add(-48 * time.Hour)
	priorSevenDayLower := now.Add(-14 * 24 * time.Hour)

	var o24Count, o24Prior, o7Count, o7Prior int
	err = s.pool.QueryRow(ctx, outcomesQuery, channelID, oneDayLower, priorOneDayLower, sevenDayLower, priorSevenDayLower).Scan(
		&o24Count, &o24Prior, &o7Count, &o7Prior,
	)
	if err != nil {
		return ChannelActivity{}, fmt.Errorf("query channel outcome counts: %w", err)
	}

	counts.Outcomes24h = OutcomeWindow{Count: o24Count, PriorCount: o24Prior, Trend: trendDirection(o24Count, o24Prior)}
	counts.Outcomes7d = OutcomeWindow{Count: o7Count, PriorCount: o7Prior, Trend: trendDirection(o7Count, o7Prior)}

	return counts, nil
}

// trendDirection classifies FR24's outcome-count comparison: TrendBetter
// when count exceeds prior, TrendWorse when it's below, and TrendSame
// otherwise -- explicitly including the 0-vs-0 case, which is "same",
// never "better".
func trendDirection(count, prior int) TrendDirection {
	switch {
	case count > prior:
		return TrendBetter
	case count < prior:
		return TrendWorse
	default:
		return TrendSame
	}
}

// activityCountsQuery is FR23's five entity-type counts over the trailing
// 24h ($3) and 7d ($2) windows, scoped to channel $1. Each CTE scans its
// table once, bounded to created_at >= $2 (the wider 7d window; Last7d is
// a superset of Last24h), and FILTERs out the 24h subset from that same
// scan -- never a second table scan for the narrower window. Boundary
// rule: lower bounds are INCLUSIVE (created_at >= boundary counts as IN
// the window); the upper bound is implicitly "now" via how $2/$3 were
// computed by the caller, never re-evaluated here.
//
// verdicts joins viability_verdict to idea for channel scoping --
// viability_verdict carries no channel_id of its own (idea does).
// video_schedule_match ("newly-linked video", FR23's load-bearing
// definition) joins to synced_video for channel scoping and counts
// vsm.created_at (when the match was FIRST recorded), never
// vsm.resolved_at -- a match recorded outside the window but
// confirmed/rejected inside it must not count, and this query never reads
// resolved_at at all so that can't happen by accident.
const activityCountsQuery = `
	WITH notes AS (
		SELECT COUNT(*) FILTER (WHERE created_at >= $3) AS c24, COUNT(*) AS c7
		FROM research_note
		WHERE channel_id = $1 AND created_at >= $2
	),
	ideas AS (
		SELECT COUNT(*) FILTER (WHERE created_at >= $3) AS c24, COUNT(*) AS c7
		FROM idea
		WHERE channel_id = $1 AND created_at >= $2
	),
	verdicts AS (
		SELECT COUNT(*) FILTER (WHERE vv.created_at >= $3) AS c24, COUNT(*) AS c7
		FROM viability_verdict vv
		JOIN idea i ON i.id = vv.idea_id
		WHERE i.channel_id = $1 AND vv.created_at >= $2
	),
	scripts AS (
		SELECT COUNT(*) FILTER (WHERE created_at >= $3) AS c24, COUNT(*) AS c7
		FROM video_script
		WHERE channel_id = $1 AND created_at >= $2
	),
	matches AS (
		SELECT COUNT(*) FILTER (WHERE vsm.created_at >= $3) AS c24, COUNT(*) AS c7
		FROM video_schedule_match vsm
		JOIN synced_video sv ON sv.id = vsm.synced_video_id
		WHERE sv.channel_id = $1 AND vsm.created_at >= $2
	)
	SELECT notes.c24, notes.c7,
	       ideas.c24, ideas.c7,
	       verdicts.c24, verdicts.c7,
	       scripts.c24, scripts.c7,
	       matches.c24, matches.c7
	FROM notes, ideas, verdicts, scripts, matches
`

// outcomesQuery is FR24's outcome count: published SyncedVideos
// (published_at IS NOT NULL) on channel $1 with at least one recorded
// video_metrics row (EXISTS, not a LATERAL join -- only presence matters,
// not the metrics value), FILTERed into the 24h window ($2..now), its
// immediately preceding 24h period ($3..$2), the 7d window ($4..now), and
// its immediately preceding 7d period ($5..$4) in a single scan bounded
// to published_at >= $5 (the widest lower bound needed).
const outcomesQuery = `
	SELECT
		COUNT(*) FILTER (WHERE sv.published_at >= $2) AS c24,
		COUNT(*) FILTER (WHERE sv.published_at >= $3 AND sv.published_at < $2) AS prior24,
		COUNT(*) FILTER (WHERE sv.published_at >= $4) AS c7,
		COUNT(*) FILTER (WHERE sv.published_at >= $5 AND sv.published_at < $4) AS prior7
	FROM synced_video sv
	WHERE sv.channel_id = $1
	  AND sv.published_at IS NOT NULL
	  AND sv.published_at >= $5
	  AND EXISTS (SELECT 1 FROM video_metrics vm WHERE vm.synced_video_id = sv.id)
`
