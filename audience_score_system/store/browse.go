// C10's cross-entity Channel-browsing reads (issue #1582, FR24): the
// prediction-vs-outcome comparison and the ideas-with-current-verdict join
// get_channel_overview and get_prediction_vs_outcome (mcp/tools/browse.go)
// need, neither of which belongs to any single entity's store.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BrowseStore covers issue #1582/FR24's cross-entity Channel-browsing
// reads. Every method here returns every matching row for its filter,
// unbounded -- get_channel_overview/get_prediction_vs_outcome
// (mcp/tools/browse.go) are the layer responsible for capping and
// reporting truncation (NFR-equivalent "bounded by construction" from
// #1582's spec), not this store.
type BrowseStore interface {
	// PredictionVsOutcome returns one PredictionOutcome row per published
	// video with a live (auto or confirmed) match to a committed
	// schedule_entry on channelID, most-recently-published first,
	// optionally narrowed to a single ideaID (nil = no filter) and/or a
	// lower/upper bound on the video's published_at (since/before, nil =
	// no filter on that side; since/before together let a caller page
	// backward past a fixed truncation limit, issue #1808). A video with
	// no recorded metrics yet does not appear -- mirrors migration 002's
	// v_prediction_vs_outcome qualifying-row rule (see that view's SQL
	// comment) with one deliberate difference: this query joins
	// schedule_entry directly to viability_verdict via
	// schedule_entry.verdict_id, never through v_current_verdict. The view
	// requires se.verdict_id to equal the idea's CURRENT verdict's id,
	// which means a schedule_entry committed under an older verdict
	// version silently drops out of the view entirely the moment a newer
	// verdict is appended for the same idea -- backwards from LB3's "bound
	// version, not a moving target" contract on that column. This method
	// returns the bound version's data regardless of what the idea's
	// current verdict has since become.
	PredictionVsOutcome(ctx context.Context, channelID uuid.UUID, ideaID *uuid.UUID, since, before *time.Time) ([]PredictionOutcome, error)

	// IdeasWithCurrentVerdict returns every Idea for channelID
	// (most-recently-created first) alongside its current verdict (the
	// v_current_verdict row for that idea, nil if none recorded yet) --
	// one round trip via LEFT JOIN rather than N+1 VerdictStore.Current
	// calls per Idea (mirrors IdeaStore.ListByChannelWithStats's rationale
	// in idea.go).
	IdeasWithCurrentVerdict(ctx context.Context, channelID uuid.UUID) ([]IdeaOverview, error)
}

// browseStore implements BrowseStore against `idea`, `schedule_entry`,
// `viability_verdict`, `video_schedule_match`, `synced_video`,
// `video_metrics` (migration 002), and `v_current_verdict`.
type browseStore struct{ pool *pgxpool.Pool }

var _ BrowseStore = browseStore{}

// predictionOutcomeJoin is the FROM/JOIN chain shared by
// BrowseStore.PredictionVsOutcome (one Channel, every qualifying row) and
// MyWorkStore.SummariesForPerson (issue #1717, many Channels, top-1-per-
// Channel): idea -> its committed schedule_entry -> the SPECIFIC
// viability_verdict version bound to that entry (LB3, not the idea's
// current verdict) -> its author -> a live (auto/confirmed)
// video_schedule_match -> the synced_video it points at -> that video's
// latest video_metrics snapshot. Factored out so this join -- and in
// particular the LB3 "bound version, not current" subtlety documented at
// length on PredictionVsOutcome's doc comment above -- is written exactly
// once; callers add their own SELECT list, WHERE, and ORDER BY/window
// function on top of it.
const predictionOutcomeJoin = `
	FROM idea i
	JOIN schedule_entry se ON se.idea_id = i.id AND se.state = 'committed'
	JOIN viability_verdict vv ON vv.id = se.verdict_id
	JOIN person author ON author.id = vv.author_person_id
	JOIN video_schedule_match vsm ON vsm.schedule_entry_id = se.id AND vsm.state IN ('auto', 'confirmed')
	JOIN synced_video sv ON sv.id = vsm.synced_video_id
	JOIN LATERAL (
		SELECT m.views, m.average_view_duration_seconds, m.average_view_percentage,
		       m.impressions, m.impression_ctr, m.measured_at
		FROM video_metrics m
		WHERE m.synced_video_id = sv.id
		ORDER BY m.measured_at DESC
		LIMIT 1
	) vm ON TRUE
`

// PredictionVsOutcome deliberately does not select from
// v_prediction_vs_outcome -- see the BrowseStore.PredictionVsOutcome doc
// for why. $2/$3/$4 use a NULL-safe "no filter" idiom (`$n IS NULL OR ...`)
// matching schedule_read.go's withinWindow-adjacent SQL style elsewhere in
// this package. since/before together let a caller page backward past a
// fixed truncation limit (issue #1808): request the newest window, then
// re-request with before set to the oldest row's published_at from the
// previous response.
func (s browseStore) PredictionVsOutcome(ctx context.Context, channelID uuid.UUID, ideaID *uuid.UUID, since, before *time.Time) ([]PredictionOutcome, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT
			i.id, i.title,
			vv.id, vv.version, vv.verdict, vv.reasoning, vv.author_person_id,
			COALESCE(NULLIF(author.display_name, ''), COALESCE(author.email, '')), vv.created_at,
			se.id, se.proposed_publish_at, se.approved_at,
			vsm.id, vsm.state, vsm.confidence,
			sv.id, sv.youtube_video_id, COALESCE(sv.title, ''), sv.published_at,
			vm.views, vm.average_view_duration_seconds, vm.average_view_percentage,
			vm.impressions, vm.impression_ctr, vm.measured_at
		`+predictionOutcomeJoin+`
		WHERE i.channel_id = $1
		  AND ($2::uuid IS NULL OR i.id = $2)
		  AND ($3::timestamptz IS NULL OR sv.published_at >= $3)
		  AND ($4::timestamptz IS NULL OR sv.published_at < $4)
		ORDER BY sv.published_at DESC NULLS LAST, i.title
	`, channelID, ideaID, since, before)
	if err != nil {
		return nil, fmt.Errorf("list prediction vs outcome for channel: %w", err)
	}
	defer rows.Close()

	var out []PredictionOutcome
	for rows.Next() {
		var r PredictionOutcome
		if err := rows.Scan(
			&r.IdeaID, &r.IdeaTitle,
			&r.VerdictID, &r.VerdictVersion, &r.Verdict, &r.VerdictReasoning, &r.VerdictAuthorPersonID,
			&r.VerdictAuthorDisplayName, &r.VerdictCreatedAt,
			&r.ScheduleEntryID, &r.ProposedPublishAt, &r.ApprovedAt,
			&r.MatchID, &r.MatchState, &r.MatchConfidence,
			&r.SyncedVideoID, &r.YouTubeVideoID, &r.VideoTitle, &r.PublishedAt,
			&r.Views, &r.AverageViewDurationSeconds, &r.AverageViewPercentage,
			&r.Impressions, &r.ImpressionCTR, &r.MetricsMeasuredAt,
		); err != nil {
			return nil, fmt.Errorf("scan prediction vs outcome row: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list prediction vs outcome for channel: %w", err)
	}
	return out, nil
}

func (s browseStore) IdeasWithCurrentVerdict(ctx context.Context, channelID uuid.UUID) ([]IdeaOverview, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT i.id, i.channel_id, i.title, i.created_by_person_id, i.created_at,
		       cv.id, cv.version, cv.verdict, cv.reasoning
		FROM idea i
		LEFT JOIN v_current_verdict cv ON cv.idea_id = i.id
		WHERE i.channel_id = $1
		ORDER BY i.created_at DESC
	`, channelID)
	if err != nil {
		return nil, fmt.Errorf("list ideas with current verdict for channel: %w", err)
	}
	defer rows.Close()

	var out []IdeaOverview
	for rows.Next() {
		var o IdeaOverview
		if err := rows.Scan(
			&o.ID, &o.ChannelID, &o.Title, &o.CreatedByPersonID, &o.CreatedAt,
			&o.CurrentVerdictID, &o.CurrentVerdictVersion, &o.CurrentVerdict, &o.CurrentVerdictReasoning,
		); err != nil {
			return nil, fmt.Errorf("scan idea with current verdict: %w", err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list ideas with current verdict for channel: %w", err)
	}
	return out, nil
}
