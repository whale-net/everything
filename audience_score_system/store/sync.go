package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PublishedVideoFilter is ListPublishedWithMetrics' combinable filter set
// (issue #2031, FR28/FR29/FR30) -- every non-zero-value field narrows the
// result set, and all set fields combine with AND (FR30). The zero value
// (TitleContains == "", PublishedFrom == nil, PublishedTo == nil,
// Synced == nil) applies no filter at all.
type PublishedVideoFilter struct {
	// TitleContains is a case-insensitive substring match against the
	// video's title (FR28); "" means no title filter.
	TitleContains string
	// PublishedFrom/PublishedTo bound the video's published_at, inclusive
	// on both ends (FR28); nil means unbounded on that side.
	PublishedFrom *time.Time
	PublishedTo   *time.Time
	// Synced is FR29's binary sync-status bucket filter: nil means no
	// filter, true restricts to videos with a live (auto or confirmed)
	// video_schedule_match, false restricts to everything else (no match
	// row, a pending row with or without a candidate, or a rejected row)
	// -- see PublishedVideoRow.Synced's doc comment for the exact rule
	// this must classify against once Implementation wires in the query.
	Synced *bool
}

// PublishedVideoRow is one row of ListPublishedWithMetrics (issue #2031,
// FR27/FR28/FR29/FR30) -- a published SyncedVideo paired with its latest
// recorded VideoMetrics (nil when none has been recorded yet -- FR27's
// explicit "no metrics recorded" placeholder, never a zero value presented
// as data) and its FR29 sync-status bucket.
type PublishedVideoRow struct {
	SyncedVideo SyncedVideo
	// Metrics is nil when no video_metrics row has been recorded yet for
	// this video (FR27) -- rendered as an explicit placeholder, never as
	// zeros.
	Metrics *VideoMetrics
	// Synced is FR29's binary bucket, confirmed against match.go's
	// "live match" predicate (the same one isVideoScriptPublished and
	// MatchStore.ListCandidates already use): true when the video has a
	// video_schedule_match row with state IN ('auto', 'confirmed');
	// false for everything else -- no match row at all, a pending row
	// (with or without a candidate video_script_id, per HasMatch's doc
	// comment), or a rejected row. There is deliberately no third state.
	Synced bool
}

// SyncStore covers `synced_video` and `video_metrics` (migration 002,
// FR14/FR21) -- the read models the Temporal sync writes into.
type SyncStore interface {
	// UpsertVideos upserts vids by their (channel_id, youtube_video_id)
	// natural key -- a re-sync updates the existing row rather than
	// duplicating it.
	UpsertVideos(ctx context.Context, channelID uuid.UUID, vids []SyncedVideo) error

	// UpsertMetrics upserts m by its (synced_video_id, measured_at)
	// natural key.
	UpsertMetrics(ctx context.Context, m []VideoMetrics) error

	// ListSchedule returns SyncedVideo rows for channelID, ordered by
	// effective publish time (PublishAt if set, else PublishedAt -- a row
	// with neither always passes any from/to window, since there is
	// nothing to compare against). from/to bound that effective timestamp
	// (nil = no bound on that side); includeDrafts=false excludes rows
	// with IsScheduledDraft=true. limit (<=0 = unbounded) caps the
	// response; truncated reports whether more matching rows exist beyond
	// it. Callers needing the complete, unbounded set for a correctness-
	// sensitive computation (get_channel_overview's synced-schedule
	// summary counts, save_schedule_draft's cadence/collision detection)
	// pass from=nil, to=nil, includeDrafts=true, limit=0 -- see
	// mcp/tools/schedule_read.go's get_channel_schedule and
	// mcp/tools/schedule_draft.go's get_drafting_context for the bounded
	// callers (issue #1812's follow-up: filtering/pagination belongs in
	// this layer, not re-implemented over an unbounded Go-side fetch).
	ListSchedule(ctx context.Context, channelID uuid.UUID, from, to *time.Time, includeDrafts bool, limit int) (vids []SyncedVideo, truncated bool, err error)

	// GetByID returns the SyncedVideo for id, or pgx.ErrNoRows if none
	// exists -- issue #1581's list_pending_matches/resolve_pending_match
	// tools resolve a video_schedule_match's SyncedVideoID through this.
	GetByID(ctx context.Context, id uuid.UUID) (SyncedVideo, error)

	// LatestMetricsFor returns the most recent VideoMetrics row (by
	// measured_at) for syncedVideoID, or nil if none has been recorded
	// yet -- issue #1581's list_pending_matches metrics snapshot.
	LatestMetricsFor(ctx context.Context, syncedVideoID uuid.UUID) (*VideoMetrics, error)

	// ListPublishedWithMetrics returns published SyncedVideos
	// (published_at IS NOT NULL) on channelID with their latest recorded
	// metrics and sync-status bucket (issue #2031, FR27/FR28/FR29/FR30),
	// most-recently-published first, narrowed by f (all set fields
	// combine with AND, FR30) and capped at limit (<=0 = unbounded;
	// truncated reports whether more matching rows exist beyond it, per
	// pagination.go's fetchLimit/paginate idiom). NFR3: the whole
	// listing -- rows, latest metrics, and sync bucket -- is answered in
	// ONE query (a LATERAL join for latest metrics per video, a LEFT JOIN
	// onto video_schedule_match for the bucket, mirroring browse.go's
	// PredictionVsOutcome/calibration.go's MonthlyTrend) -- never a
	// Go-side loop calling LatestMetricsFor or MatchStore.HasMatch once
	// per video.
	ListPublishedWithMetrics(ctx context.Context, channelID uuid.UUID, f PublishedVideoFilter, limit int) (rows []PublishedVideoRow, truncated bool, err error)
}

// syncStore implements SyncStore against `synced_video` and
// `video_metrics` (migration 002).
type syncStore struct{ pool *pgxpool.Pool }

var _ SyncStore = syncStore{}

const syncedVideoColumns = `id, channel_id, youtube_video_id, COALESCE(title, ''), privacy_status, publish_at, published_at, is_scheduled_draft, last_synced_at`

// syncedVideoColumnsQualified is syncedVideoColumns' sv.-qualified twin,
// for ListPublishedWithMetrics' join (publishedVideoJoin) alone -- unlike
// every other query in this file, that one joins video_metrics and
// video_schedule_match, both of which also have an `id` column, so the
// unqualified list above would be ambiguous.
const syncedVideoColumnsQualified = `sv.id, sv.channel_id, sv.youtube_video_id, COALESCE(sv.title, ''), sv.privacy_status, sv.publish_at, sv.published_at, sv.is_scheduled_draft, sv.last_synced_at`

func scanSyncedVideo(row pgx.Row) (SyncedVideo, error) {
	var v SyncedVideo
	err := row.Scan(&v.ID, &v.ChannelID, &v.YouTubeVideoID, &v.Title, &v.PrivacyStatus, &v.PublishAt, &v.PublishedAt, &v.IsScheduledDraft, &v.LastSyncedAt)
	return v, err
}

// UpsertVideos relies on ON CONFLICT (channel_id, youtube_video_id) DO
// UPDATE so a re-sync of a video already on file updates that row rather
// than duplicating it.
func (s syncStore) UpsertVideos(ctx context.Context, channelID uuid.UUID, vids []SyncedVideo) error {
	if len(vids) == 0 {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	for _, v := range vids {
		if _, err := tx.Exec(ctx, `
			INSERT INTO synced_video (channel_id, youtube_video_id, title, privacy_status, publish_at, published_at, is_scheduled_draft, last_synced_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (channel_id, youtube_video_id) DO UPDATE
				SET title = EXCLUDED.title,
					privacy_status = EXCLUDED.privacy_status,
					publish_at = EXCLUDED.publish_at,
					published_at = EXCLUDED.published_at,
					is_scheduled_draft = EXCLUDED.is_scheduled_draft,
					last_synced_at = EXCLUDED.last_synced_at
		`, channelID, v.YouTubeVideoID, v.Title, v.PrivacyStatus, v.PublishAt, v.PublishedAt, v.IsScheduledDraft, v.LastSyncedAt); err != nil {
			return fmt.Errorf("upsert synced_video %s: %w", v.YouTubeVideoID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// UpsertMetrics relies on ON CONFLICT (synced_video_id, measured_at) DO
// UPDATE so re-measuring the same video at the same instant updates rather
// than duplicates.
func (s syncStore) UpsertMetrics(ctx context.Context, m []VideoMetrics) error {
	if len(m) == 0 {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	for _, vm := range m {
		if _, err := tx.Exec(ctx, `
			INSERT INTO video_metrics (synced_video_id, views, average_view_duration_seconds, average_view_percentage, impressions, impression_ctr, measured_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (synced_video_id, measured_at) DO UPDATE
				SET views = EXCLUDED.views,
					average_view_duration_seconds = EXCLUDED.average_view_duration_seconds,
					average_view_percentage = EXCLUDED.average_view_percentage,
					impressions = EXCLUDED.impressions,
					impression_ctr = EXCLUDED.impression_ctr
		`, vm.SyncedVideoID, vm.Views, vm.AverageViewDurationSeconds, vm.AverageViewPercentage, vm.Impressions, vm.ImpressionCTR, vm.MeasuredAt); err != nil {
			return fmt.Errorf("upsert video_metrics for %s: %w", vm.SyncedVideoID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// GetByID returns the SyncedVideo for id, or pgx.ErrNoRows if none exists.
func (s syncStore) GetByID(ctx context.Context, id uuid.UUID) (SyncedVideo, error) {
	v, err := scanSyncedVideo(s.pool.QueryRow(ctx, `SELECT `+syncedVideoColumns+` FROM synced_video WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SyncedVideo{}, pgx.ErrNoRows
		}
		return SyncedVideo{}, fmt.Errorf("get synced_video by id: %w", err)
	}
	return v, nil
}

// LatestMetricsFor returns the most recent video_metrics row (by
// measured_at) for syncedVideoID, or (nil, nil) if none has been recorded
// yet -- never an error for "no metrics yet", since a just-matched video
// may not have completed its first outcome sync cycle.
func (s syncStore) LatestMetricsFor(ctx context.Context, syncedVideoID uuid.UUID) (*VideoMetrics, error) {
	var m VideoMetrics
	err := s.pool.QueryRow(ctx, `
		SELECT id, synced_video_id, views, average_view_duration_seconds, average_view_percentage, impressions, impression_ctr, measured_at
		FROM video_metrics
		WHERE synced_video_id = $1
		ORDER BY measured_at DESC
		LIMIT 1
	`, syncedVideoID).Scan(&m.ID, &m.SyncedVideoID, &m.Views, &m.AverageViewDurationSeconds, &m.AverageViewPercentage, &m.Impressions, &m.ImpressionCTR, &m.MeasuredAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get latest video_metrics for synced_video %s: %w", syncedVideoID, err)
	}
	return &m, nil
}

// ListSchedule's WHERE clause mirrors the old mcp/tools/schedule_read.go
// withinWindow helper exactly (a row with no effective timestamp always
// passes the from/to window) plus an is_scheduled_draft filter, all pushed
// to SQL so LIMIT can be applied correctly alongside them (issue #1812's
// follow-up) -- see ListSchedule's doc comment for full parameter
// semantics.
func (s syncStore) ListSchedule(ctx context.Context, channelID uuid.UUID, from, to *time.Time, includeDrafts bool, limit int) ([]SyncedVideo, bool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+syncedVideoColumns+`
		FROM synced_video
		WHERE channel_id = $1
		  AND (
		    COALESCE(publish_at, published_at) IS NULL
		    OR (
		      ($2::timestamptz IS NULL OR COALESCE(publish_at, published_at) >= $2)
		      AND ($3::timestamptz IS NULL OR COALESCE(publish_at, published_at) <= $3)
		    )
		  )
		  AND (NOT is_scheduled_draft OR $4)
		ORDER BY COALESCE(publish_at, published_at)
		LIMIT $5
	`, channelID, from, to, includeDrafts, fetchLimit(limit))
	if err != nil {
		return nil, false, fmt.Errorf("list synced videos by channel: %w", err)
	}
	defer rows.Close()

	var vids []SyncedVideo
	for rows.Next() {
		v, err := scanSyncedVideo(rows)
		if err != nil {
			return nil, false, fmt.Errorf("scan synced_video: %w", err)
		}
		vids = append(vids, v)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("list synced videos by channel: %w", err)
	}
	vids, truncated := paginate(vids, limit)
	return vids, truncated, nil
}

// publishedVideoJoin is ListPublishedWithMetrics' FROM/JOIN chain (issue
// #2031, NFR3): synced_video -> a LEFT JOIN LATERAL for its single latest
// video_metrics row (nil when none recorded yet, FR27 -- LEFT, not the
// plain JOIN LATERAL predictionOutcomeJoin (browse.go) uses, since a
// video with no metrics must still appear here) -> a LEFT JOIN directly
// onto video_schedule_match, filtered in the join condition itself to
// state IN ('auto', 'confirmed') (FR29's "live match" predicate --
// exactly isVideoScriptPublished's/MatchStore.ListCandidates' predicate,
// video_script.go/match.go). Filtering the state inside the join
// condition rather than in the WHERE clause is what makes this a LEFT
// JOIN safe from fan-out: migration 002's partial unique index on
// video_schedule_match(synced_video_id) WHERE state != 'rejected'
// guarantees at most one non-rejected row per synced_video_id, and
// 'auto'/'confirmed' are both non-rejected, so at most one row can ever
// satisfy this join condition -- vsm.id IS NOT NULL is therefore exactly
// FR29's binary Synced bucket, never a duplicated synced_video row.
const publishedVideoJoin = `
	FROM synced_video sv
	LEFT JOIN LATERAL (
		SELECT m.id, m.synced_video_id, m.views, m.average_view_duration_seconds,
		       m.average_view_percentage, m.impressions, m.impression_ctr, m.measured_at
		FROM video_metrics m
		WHERE m.synced_video_id = sv.id
		ORDER BY m.measured_at DESC
		LIMIT 1
	) vm ON TRUE
	LEFT JOIN video_schedule_match vsm
		ON vsm.synced_video_id = sv.id AND vsm.state IN ('auto', 'confirmed')
`

// ListPublishedWithMetrics -- see the SyncStore.ListPublishedWithMetrics
// doc comment and publishedVideoJoin's doc comment above for the exact
// shape and why it is fan-out-safe. $2/$3/$4/$5 use the same NULL-safe
// "no filter" idiom as PredictionVsOutcome (browse.go); $6 is
// fetchLimit(limit) (pagination.go). Title filtering is case-insensitive
// (FR28) via ILIKE; f.TitleContains == "" passes NULL for $2 so the
// filter is skipped entirely rather than matching an empty substring
// (which would trivially match every row anyway, but this avoids
// building an empty-substring LIKE pattern for no reason).
func (s syncStore) ListPublishedWithMetrics(ctx context.Context, channelID uuid.UUID, f PublishedVideoFilter, limit int) ([]PublishedVideoRow, bool, error) {
	var titleContains *string
	if f.TitleContains != "" {
		titleContains = &f.TitleContains
	}

	rows, err := s.pool.Query(ctx, `
		SELECT `+syncedVideoColumnsQualified+`,
			vm.id, vm.synced_video_id, vm.views, vm.average_view_duration_seconds,
			vm.average_view_percentage, vm.impressions, vm.impression_ctr, vm.measured_at,
			(vsm.id IS NOT NULL) AS synced
		`+publishedVideoJoin+`
		WHERE sv.channel_id = $1
		  AND sv.published_at IS NOT NULL
		  AND ($2::text IS NULL OR sv.title ILIKE '%' || $2 || '%')
		  AND ($3::timestamptz IS NULL OR sv.published_at >= $3)
		  AND ($4::timestamptz IS NULL OR sv.published_at <= $4)
		  AND ($5::boolean IS NULL OR (vsm.id IS NOT NULL) = $5)
		ORDER BY sv.published_at DESC
		LIMIT $6
	`, channelID, titleContains, f.PublishedFrom, f.PublishedTo, f.Synced, fetchLimit(limit))
	if err != nil {
		return nil, false, fmt.Errorf("list published videos with metrics for channel: %w", err)
	}
	defer rows.Close()

	var out []PublishedVideoRow
	for rows.Next() {
		var r PublishedVideoRow
		var metricsID, metricsSyncedVideoID *uuid.UUID
		var views, impressions *int64
		var avgDuration, avgPercentage, impressionCTR *float64
		var measuredAt *time.Time

		if err := rows.Scan(
			&r.SyncedVideo.ID, &r.SyncedVideo.ChannelID, &r.SyncedVideo.YouTubeVideoID, &r.SyncedVideo.Title,
			&r.SyncedVideo.PrivacyStatus, &r.SyncedVideo.PublishAt, &r.SyncedVideo.PublishedAt,
			&r.SyncedVideo.IsScheduledDraft, &r.SyncedVideo.LastSyncedAt,
			&metricsID, &metricsSyncedVideoID, &views, &avgDuration, &avgPercentage, &impressions, &impressionCTR, &measuredAt,
			&r.Synced,
		); err != nil {
			return nil, false, fmt.Errorf("scan published video with metrics row: %w", err)
		}

		// metricsID is nil exactly when the LEFT JOIN LATERAL found no
		// video_metrics row for this video (FR27) -- rendered by `web` as
		// an explicit placeholder, never as a zero value presented as
		// data.
		if metricsID != nil {
			r.Metrics = &VideoMetrics{
				ID:                         *metricsID,
				SyncedVideoID:              *metricsSyncedVideoID,
				Views:                      views,
				AverageViewDurationSeconds: avgDuration,
				AverageViewPercentage:      avgPercentage,
				Impressions:                impressions,
				ImpressionCTR:              impressionCTR,
				MeasuredAt:                 *measuredAt,
			}
		}

		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("list published videos with metrics for channel: %w", err)
	}
	out, truncated := paginate(out, limit)
	return out, truncated, nil
}
