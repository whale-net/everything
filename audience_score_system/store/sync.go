package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

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

	// ListSchedule returns every SyncedVideo for channelID.
	ListSchedule(ctx context.Context, channelID uuid.UUID) ([]SyncedVideo, error)

	// GetByID returns the SyncedVideo for id, or pgx.ErrNoRows if none
	// exists -- issue #1581's list_pending_matches/resolve_pending_match
	// tools resolve a video_schedule_match's SyncedVideoID through this.
	GetByID(ctx context.Context, id uuid.UUID) (SyncedVideo, error)

	// LatestMetricsFor returns the most recent VideoMetrics row (by
	// measured_at) for syncedVideoID, or nil if none has been recorded
	// yet -- issue #1581's list_pending_matches metrics snapshot.
	LatestMetricsFor(ctx context.Context, syncedVideoID uuid.UUID) (*VideoMetrics, error)
}

// syncStore implements SyncStore against `synced_video` and
// `video_metrics` (migration 002).
type syncStore struct{ pool *pgxpool.Pool }

var _ SyncStore = syncStore{}

const syncedVideoColumns = `id, channel_id, youtube_video_id, COALESCE(title, ''), privacy_status, publish_at, published_at, is_scheduled_draft, last_synced_at`

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

func (s syncStore) ListSchedule(ctx context.Context, channelID uuid.UUID) ([]SyncedVideo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+syncedVideoColumns+`
		FROM synced_video
		WHERE channel_id = $1
		ORDER BY COALESCE(publish_at, published_at)
	`, channelID)
	if err != nil {
		return nil, fmt.Errorf("list synced videos by channel: %w", err)
	}
	defer rows.Close()

	var vids []SyncedVideo
	for rows.Next() {
		v, err := scanSyncedVideo(rows)
		if err != nil {
			return nil, fmt.Errorf("scan synced_video: %w", err)
		}
		vids = append(vids, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list synced videos by channel: %w", err)
	}
	return vids, nil
}
