package sync

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/youtube"
)

// SyncOutcomes is C9's real outcome-sync activity (issue #1581, FR21-FR23):
// for every `synced_video` on channelID with a non-null published_at,
// calls youtube.Client.Metrics (#1573) and upserts `video_metrics`
// (a.Sync.UpsertMetrics, migration 002/#1569) keyed on (synced_video_id,
// measured_at) -- an append of measurements over time, never a destructive
// overwrite (see "Metrics accumulate" below) -- then, for a video with no
// existing SETTLED video_schedule_match row (a.Matches.HasMatch, see its
// doc comment), scores it against the Channel's committed, still-unmatched
// schedule entries (a.Matches.ListCandidates, matching.go's Match) and
// records an 'auto' match at or above matching.MatchConfidenceThreshold or
// a 'pending' one below it (FR22/FR23 -- never auto-links below the
// threshold, including when there is no plausible candidate at all).
//
// # Metrics accumulate, never overwrite
//
// a.Sync.UpsertMetrics upserts on (synced_video_id, measured_at): running
// this activity twice within the same measurement instant converges on one
// row (idempotent), but two runs at genuinely different times produce two
// distinct video_metrics rows for the same video -- the append-only history
// v_prediction_vs_outcome (migration 002) reads the latest of.
//
// # Matching is idempotent, except for the no-candidate-at-all placeholder
//
// a.Matches.HasMatch gates matching per video BEFORE scoring: a video that
// already has a SETTLED video_schedule_match row -- auto, confirmed, or
// rejected in any case, or pending with a real schedule_entry_id -- is
// skipped entirely on a later cycle. This is deliberate for 'rejected' too:
// FR23's default is that a rejected match's video stays unmatched, not that
// it gets automatically re-queued next cycle (that would require an
// explicit future re-queue tool, not implemented in M1). Metrics still
// refresh for every published video regardless of match state -- only
// matching itself is gated.
//
// A pending row with schedule_entry_id == nil (no committed schedule_entry
// existed as a candidate at all when this video was first scored -- e.g. a
// backdated/historical video synced before its schedule_entry was
// committed, issue #1652) is NOT settled: HasMatch reports false for it, so
// this video is re-scored on every later cycle until either a real
// candidate appears (a.Matches.Record then updates that same row in place,
// see its doc comment) or a human rejects it via resolve_pending_match.
//
// # Error handling (FR4/FR21)
//
// youtube.ErrRevoked maps to a.Tokens.MarkNeedsReauth followed by a
// temporal.NewNonRetryableApplicationError of RevokedErrorType, exactly
// like SyncSchedule (video_sync.go) -- ChannelSyncWorkflow's isRevoked
// branch (workflow.go) relies on both, and this activity runs strictly
// after SyncSchedule in the same workflow run (workflow.go), so a
// mid-outcome-sync revocation still leaves whatever SyncSchedule already
// wrote this cycle intact. youtube.ErrQuotaExceeded/ErrTransient/
// ErrPermanent instead surface as a plain wrapped error -- retryable,
// bounded by defaultActivityOptions' RetryPolicy (workflow.go).
func (a *Activities) SyncOutcomes(ctx context.Context, channelID uuid.UUID) error {
	ch, err := a.Channels.GetByID(ctx, channelID)
	if err != nil {
		return fmt.Errorf("%s: load channel %s: %w", ActivitySyncOutcomes, channelID, err)
	}

	videos, err := a.Sync.ListSchedule(ctx, channelID)
	if err != nil {
		return fmt.Errorf("%s: list synced videos for channel %s: %w", ActivitySyncOutcomes, channelID, err)
	}

	published := make([]store.SyncedVideo, 0, len(videos))
	for _, v := range videos {
		if v.PublishedAt != nil {
			published = append(published, v)
		}
	}
	if len(published) == 0 {
		logger.Info("outcome sync cycle complete: no published videos", "channel_id", channelID.String())
		return nil
	}

	ts, err := a.Tokens.TokenSource(ctx, channelID)
	if err != nil {
		return fmt.Errorf("%s: token source for channel %s: %w", ActivitySyncOutcomes, channelID, err)
	}
	yt := a.NewYouTubeClient(ts)

	if err := a.syncMetrics(ctx, channelID, ch.YouTubeChannelID, yt, published); err != nil {
		return err
	}

	matched, pending, err := a.syncMatches(ctx, channelID, published)
	if err != nil {
		return err
	}

	logger.Info("outcome sync cycle complete",
		"channel_id", channelID.String(),
		"published_videos", len(published),
		"videos_auto_matched", matched,
		"videos_queued_pending", pending,
	)
	return nil
}

// syncMetrics calls youtube.Client.Metrics for every published video and
// upserts the results into `video_metrics`.
func (a *Activities) syncMetrics(ctx context.Context, channelID uuid.UUID, youtubeChannelID string, yt youtube.Client, published []store.SyncedVideo) error {
	videoIDs := make([]string, 0, len(published))
	since := published[0].PublishedAt
	for _, v := range published {
		videoIDs = append(videoIDs, v.YouTubeVideoID)
		if v.PublishedAt.Before(*since) {
			since = v.PublishedAt
		}
	}

	results, err := yt.Metrics(ctx, youtubeChannelID, videoIDs, *since)
	if err != nil {
		return a.syncOutcomesError(ctx, channelID, err)
	}

	bySyncedVideoID := make(map[string]uuid.UUID, len(published))
	for _, v := range published {
		bySyncedVideoID[v.YouTubeVideoID] = v.ID
	}

	metrics := make([]store.VideoMetrics, 0, len(results))
	for _, m := range results {
		syncedVideoID, ok := bySyncedVideoID[m.YouTubeVideoID]
		if !ok {
			// Metrics preserves videoIDs' order/membership exactly
			// (youtube.Client.Metrics' contract) -- this should never
			// happen, but skip rather than write an orphaned metrics row
			// if it somehow does.
			continue
		}
		metrics = append(metrics, store.VideoMetrics{
			SyncedVideoID:              syncedVideoID,
			Views:                      m.Views,
			AverageViewDurationSeconds: m.AverageViewDurationSeconds,
			AverageViewPercentage:      m.AverageViewPercentage,
			Impressions:                m.Impressions,
			ImpressionCTR:              m.ImpressionCTR,
			MeasuredAt:                 m.MeasuredAt,
		})
	}

	if err := a.Sync.UpsertMetrics(ctx, metrics); err != nil {
		return fmt.Errorf("%s: upsert video metrics for channel %s: %w", ActivitySyncOutcomes, channelID, err)
	}
	return nil
}

// syncMatches scores every published video with no existing SETTLED
// video_schedule_match row against channelID's still-unmatched committed
// schedule entries (matching.Match) and records an 'auto' or 'pending'
// match for each -- see SyncOutcomes' doc comment's "Matching is
// idempotent, except for the no-candidate-at-all placeholder" section for
// why HasMatch gates this per video, and why ListCandidates is re-queried
// inside the loop (so a video matched earlier in the same cycle is never
// offered as a candidate again this cycle, preventing two different
// published videos from both claiming the same entry in one run).
func (a *Activities) syncMatches(ctx context.Context, channelID uuid.UUID, published []store.SyncedVideo) (matched, pending int, err error) {
	for _, v := range published {
		has, err := a.Matches.HasMatch(ctx, v.ID)
		if err != nil {
			return matched, pending, fmt.Errorf("%s: check existing match for video %s: %w", ActivitySyncOutcomes, v.ID, err)
		}
		if has {
			continue
		}

		candidateRows, err := a.Matches.ListCandidates(ctx, channelID)
		if err != nil {
			return matched, pending, fmt.Errorf("%s: list match candidates for channel %s: %w", ActivitySyncOutcomes, channelID, err)
		}
		candidates := make([]ScheduleEntry, len(candidateRows))
		for i, c := range candidateRows {
			candidates[i] = ScheduleEntry{ID: c.ScheduleEntryID, Title: c.IdeaTitle, ProposedPublishAt: c.ProposedPublishAt}
		}

		best, confidence, ok := Match(SyncedVideo{Title: v.Title, PublishedAt: *v.PublishedAt}, candidates)

		state := store.MatchStatePending
		var scheduleEntryID *uuid.UUID
		if ok {
			id := best.ID
			scheduleEntryID = &id
			if confidence >= MatchConfidenceThreshold {
				state = store.MatchStateAuto
			}
		}

		if err := a.Matches.Record(ctx, store.VideoScheduleMatch{
			SyncedVideoID:   v.ID,
			ScheduleEntryID: scheduleEntryID,
			Confidence:      confidence,
			State:           state,
		}); err != nil {
			return matched, pending, fmt.Errorf("%s: record match for video %s: %w", ActivitySyncOutcomes, v.ID, err)
		}

		if state == store.MatchStateAuto {
			matched++
			logger.Info("video auto-matched to schedule entry", "channel_id", channelID.String(), "synced_video_id", v.ID, "schedule_entry_id", scheduleEntryID, "confidence", confidence)
		} else {
			pending++
			logger.Warn("video queued for pending match review", "channel_id", channelID.String(), "synced_video_id", v.ID, "candidate_schedule_entry_id", scheduleEntryID, "confidence", confidence)
		}
	}
	return matched, pending, nil
}

// syncOutcomesError classifies a youtube.Client.Metrics failure exactly
// like SyncSchedule's syncScheduleError (video_sync.go): youtube.ErrRevoked
// marks channelID needs-reauth and returns a non-retryable RevokedErrorType
// error; every other classification returns a plain wrapped error,
// retryable per defaultActivityOptions.
func (a *Activities) syncOutcomesError(ctx context.Context, channelID uuid.UUID, err error) error {
	if !errors.Is(err, youtube.ErrRevoked) {
		return fmt.Errorf("%s: get metrics for channel %s: %w", ActivitySyncOutcomes, channelID, err)
	}

	if markErr := a.Tokens.MarkNeedsReauth(ctx, channelID, "revoked"); markErr != nil {
		return fmt.Errorf("%s: channel %s: mark needs-reauth after revoked credential: %w", ActivitySyncOutcomes, channelID, markErr)
	}

	return temporal.NewNonRetryableApplicationError(
		fmt.Sprintf("%s: channel %s: youtube credential revoked", ActivitySyncOutcomes, channelID),
		RevokedErrorType,
		err,
	)
}
