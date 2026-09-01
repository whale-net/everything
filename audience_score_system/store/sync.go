package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
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
}

// syncStore implements SyncStore against `synced_video` and
// `video_metrics` (migration 002).
//
// Scaffold only -- every method below is a stub. Full implementation lands
// in the Implementation phase (issue #1569's "Implementation" scope).
type syncStore struct{ pool *pgxpool.Pool }

var _ SyncStore = syncStore{}

func (s syncStore) UpsertVideos(ctx context.Context, channelID uuid.UUID, vids []SyncedVideo) error {
	return errors.New("not implemented")
}

func (s syncStore) UpsertMetrics(ctx context.Context, m []VideoMetrics) error {
	return errors.New("not implemented")
}

func (s syncStore) ListSchedule(ctx context.Context, channelID uuid.UUID) ([]SyncedVideo, error) {
	return nil, errors.New("not implemented")
}
