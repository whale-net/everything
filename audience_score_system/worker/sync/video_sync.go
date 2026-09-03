package sync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/youtube"
	"github.com/whale-net/everything/libs/go/logging"
)

var logger = logging.Get("audience_score_system/worker/sync")

// SyncSchedule is C6's real schedule-sync activity (issue #1576,
// FR14/FR15): builds a youtube.Client for channelID's current OAuth grant
// (a.Tokens.TokenSource, #1571), calls youtube.Client.ListSchedule (#1573)
// -- public uploads AND Studio scheduled/private drafts -- and upserts
// every result into `synced_video` (a.Sync.UpsertVideos, migration
// 002/#1569) by its (channel_id, youtube_video_id) natural key. Never
// deletes or reinserts: synced_video.id is referenced by
// video_schedule_match (#1581), so churning ids would break that FK.
//
// # Disappeared videos
//
// A video that no longer appears in YouTube's response is deliberately
// NOT deleted or otherwise mutated: UpsertVideos only ever touches rows
// for videos the current ListSchedule call actually returned, so a
// disappeared video's existing row is simply left as-is, its
// last_synced_at frozen at whatever cycle last saw it. A caller can tell
// "still on file, not re-confirmed this cycle" from a stale
// last_synced_at compared against the Channel's other rows, with no
// dedicated disappeared_at column or migration needed for M1 -- see
// ../../ARCHITECTURE.md's "Data model" section for this decision.
//
// # Error handling (FR4/FR14)
//
// youtube.ErrRevoked maps to a.Tokens.MarkNeedsReauth followed by a
// temporal.NewNonRetryableApplicationError of RevokedErrorType (see
// syncScheduleError) -- ChannelSyncWorkflow's isRevoked branch
// (workflow.go) relies on both: the mark so a reconnect is what clears
// needs_reauth, the non-retryable classification so a revoked credential
// never burns further YouTube quota retrying.
// youtube.ErrQuotaExceeded/ErrTransient/ErrPermanent instead surface as a
// plain wrapped error -- retryable, bounded by defaultActivityOptions'
// RetryPolicy (workflow.go).
//
// Runs entirely within the activity's own context deadline
// (defaultActivityOptions.StartToCloseTimeout, workflow.go) -- every
// YouTube API call ListSchedule issues is itself bounded by
// youtube.Client's own per-call timeout, so this method never blocks
// indefinitely.
func (a *Activities) SyncSchedule(ctx context.Context, channelID uuid.UUID) error {
	ch, err := a.Channels.GetByID(ctx, channelID)
	if err != nil {
		return fmt.Errorf("%s: load channel %s: %w", ActivitySyncSchedule, channelID, err)
	}

	ts, err := a.Tokens.TokenSource(ctx, channelID)
	if err != nil {
		return fmt.Errorf("%s: token source for channel %s: %w", ActivitySyncSchedule, channelID, err)
	}

	yt := a.NewYouTubeClient(ts)
	videos, err := yt.ListSchedule(ctx, ch.YouTubeChannelID)
	if err != nil {
		return a.syncScheduleError(ctx, channelID, err)
	}

	now := time.Now()
	drafts := 0
	synced := make([]store.SyncedVideo, 0, len(videos))
	for _, v := range videos {
		if v.IsScheduledDraft {
			drafts++
		}
		synced = append(synced, store.SyncedVideo{
			ChannelID:        channelID,
			YouTubeVideoID:   v.YouTubeVideoID,
			Title:            v.Title,
			PrivacyStatus:    v.PrivacyStatus,
			PublishAt:        v.PublishAt,
			PublishedAt:      v.PublishedAt,
			IsScheduledDraft: v.IsScheduledDraft,
			LastSyncedAt:     now,
		})
	}

	if err := a.Sync.UpsertVideos(ctx, channelID, synced); err != nil {
		return fmt.Errorf("%s: upsert synced videos for channel %s: %w", ActivitySyncSchedule, channelID, err)
	}

	// Per-cycle summary only -- never a token or credential value (none
	// are in scope here: yt only ever consumes ts, this method never sees
	// the token itself).
	logger.Info("schedule sync cycle complete",
		"channel_id", channelID.String(),
		"videos_seen", len(videos),
		"videos_upserted", len(synced),
		"drafts", drafts,
	)
	return nil
}

// syncScheduleError classifies a youtube.Client.ListSchedule failure per
// SyncSchedule's doc comment: youtube.ErrRevoked marks channelID
// needs-reauth and returns a non-retryable RevokedErrorType error; every
// other classification (quota/transient/permanent) returns a plain
// wrapped error, retryable per defaultActivityOptions.
func (a *Activities) syncScheduleError(ctx context.Context, channelID uuid.UUID, err error) error {
	if !errors.Is(err, youtube.ErrRevoked) {
		return fmt.Errorf("%s: list schedule for channel %s: %w", ActivitySyncSchedule, channelID, err)
	}

	if markErr := a.Tokens.MarkNeedsReauth(ctx, channelID, "revoked"); markErr != nil {
		return fmt.Errorf("%s: channel %s: mark needs-reauth after revoked credential: %w", ActivitySyncSchedule, channelID, markErr)
	}

	return temporal.NewNonRetryableApplicationError(
		fmt.Sprintf("%s: channel %s: youtube credential revoked", ActivitySyncSchedule, channelID),
		RevokedErrorType,
		err,
	)
}
