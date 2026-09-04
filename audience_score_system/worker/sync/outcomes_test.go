//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it -- mirrors video_sync_test.go's pattern exactly: spin up a throwaway
// Postgres via dbtest, apply the real embedded migrations, drive
// SyncOutcomes (outcomes.go) against it with a youtube/fake.Client
// standing in for the real YouTube Analytics API, and assert against both
// the real store.MatchStore/store.SyncStore rows and the real
// v_prediction_vs_outcome view (migration 002, FR24) -- the same LB3 chain
// end-to-end this task's Testing section calls for.
package sync

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/youtube"
	"github.com/whale-net/everything/audience_score_system/youtube/fake"
	"github.com/whale-net/everything/libs/go/dbtest"
)

// outcomesFixture is the common setup every test below needs: a Channel
// with a live creator, ready for a committed schedule entry (matching
// candidate) and a published synced_video (the thing to match against it).
// Deliberately creates its own Channel/Person (rather than reusing
// setupSyncChannel, which does not hand the creator id back) so
// committedEntry never needs to re-derive it via a role lookup.
type outcomesFixture struct {
	st      *store.Store
	db      *dbtest.Postgres
	ch      store.Channel
	creator store.Person
}

func newOutcomesFixture(t *testing.T) *outcomesFixture {
	t.Helper()
	ctx := context.Background()

	st, db := newSyncTestStack(t)

	creator, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "creator@example.com", "Creator")
	require.NoError(t, err)
	ch, err := st.Channels().Create(ctx, "yt-"+uuid.NewString(), "Channel", creator.ID)
	require.NoError(t, err)

	return &outcomesFixture{st: st, db: db, ch: ch, creator: creator}
}

// committedEntry creates an Idea -> viable Verdict -> committed
// schedule_entry chain (LB3's record chain) on f.ch, bound to title/
// proposedPublishAt -- the matcher's candidate pool (MatchStore.
// ListCandidates only returns committed entries).
func (f *outcomesFixture) committedEntry(t *testing.T, ctx context.Context, title string, proposedPublishAt time.Time) store.ScheduleEntry {
	t.Helper()

	idea, err := f.st.Ideas().Create(ctx, f.ch.ID, title, f.creator.ID)
	require.NoError(t, err)
	verdict, err := f.st.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "greenlit for outcomes test", AuthorPersonID: f.creator.ID,
	})
	require.NoError(t, err)
	entry, err := f.st.Schedules().SaveDraft(ctx, store.SaveDraftInput{
		ChannelID: f.ch.ID, IdeaID: idea.ID, VerdictID: verdict.ID,
		ProposedPublishAt: proposedPublishAt, CreatedByPersonID: f.creator.ID,
	})
	require.NoError(t, err)
	require.NoError(t, f.st.Schedules().Approve(ctx, entry.ID, f.creator.ID))

	got, err := f.st.Schedules().GetByID(ctx, entry.ID)
	require.NoError(t, err)
	return got
}

// syncedVideo upserts a single published synced_video row directly (no
// SyncSchedule call needed -- SyncOutcomes only ever reads from SyncStore,
// it never talks to YouTube's Data API) and returns it with its generated
// ID.
func (f *outcomesFixture) syncedVideo(t *testing.T, ctx context.Context, youtubeVideoID, title string, publishedAt time.Time) store.SyncedVideo {
	t.Helper()
	require.NoError(t, f.st.Sync().UpsertVideos(ctx, f.ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: youtubeVideoID, Title: title,
		PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: &publishedAt, LastSyncedAt: time.Now(),
	}}))
	rows, _, err := f.st.Sync().ListSchedule(ctx, f.ch.ID, nil, nil, true, 0)
	require.NoError(t, err)
	for _, r := range rows {
		if r.YouTubeVideoID == youtubeVideoID {
			return r
		}
	}
	t.Fatalf("synced_video %s not found after upsert", youtubeVideoID)
	return store.SyncedVideo{}
}

// singleMatchFor queries video_schedule_match directly for exactly one row
// belonging to syncedVideoID -- MatchStore has no "get by synced_video_id"
// method (GetByID takes the match's own id, which no test here has yet),
// so this is the same "reach past the store API for a raw assertion"
// pattern store_integration_test.go/video_sync_test.go both use elsewhere.
func (f *outcomesFixture) singleMatchFor(t *testing.T, ctx context.Context, syncedVideoID uuid.UUID) store.VideoScheduleMatch {
	t.Helper()
	var id uuid.UUID
	err := f.db.Pool.QueryRow(ctx, `SELECT id FROM video_schedule_match WHERE synced_video_id = $1`, syncedVideoID).Scan(&id)
	require.NoError(t, err, "expected exactly one video_schedule_match row for synced_video %s", syncedVideoID)
	m, err := f.st.Matches().GetByID(ctx, id)
	require.NoError(t, err)
	return m
}

// predictionVsOutcomeRowsForChannel duplicates
// store_integration_test.go's helper of the same purpose (unexported in
// that package) -- reads v_prediction_vs_outcome directly since
// store.PredictionVsOutcome has no dedicated store method yet.
func predictionVsOutcomeRowsForChannel(t *testing.T, ctx context.Context, db *dbtest.Postgres, channelID uuid.UUID) []store.PredictionVsOutcome {
	t.Helper()

	rows, err := db.Pool.Query(ctx, `
		SELECT idea_id, channel_id, idea_title, verdict_id, verdict_version, verdict, verdict_reasoning,
		       schedule_entry_id, proposed_publish_at, approved_at, match_id, match_state, match_confidence,
		       synced_video_id, youtube_video_id, COALESCE(video_title, ''), published_at,
		       views, average_view_duration_seconds, average_view_percentage, impressions, impression_ctr,
		       metrics_measured_at
		FROM v_prediction_vs_outcome
		WHERE channel_id = $1
		ORDER BY idea_title
	`, channelID)
	require.NoError(t, err)
	defer rows.Close()

	var out []store.PredictionVsOutcome
	for rows.Next() {
		var r store.PredictionVsOutcome
		require.NoError(t, rows.Scan(
			&r.IdeaID, &r.ChannelID, &r.IdeaTitle, &r.VerdictID, &r.VerdictVersion, &r.Verdict, &r.VerdictReasoning,
			&r.ScheduleEntryID, &r.ProposedPublishAt, &r.ApprovedAt, &r.MatchID, &r.MatchState, &r.MatchConfidence,
			&r.SyncedVideoID, &r.YouTubeVideoID, &r.VideoTitle, &r.PublishedAt,
			&r.Views, &r.AverageViewDurationSeconds, &r.AverageViewPercentage, &r.Impressions, &r.ImpressionCTR,
			&r.MetricsMeasuredAt,
		))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

func ptrInt64(v int64) *int64 { return &v }

// ── metrics upsert + auto match + LB3 chain end-to-end ──────────────────────

func TestSyncOutcomes_AboveThreshold_OneAutoMatch_AppearsInPredictionVsOutcome(t *testing.T) {
	ctx := context.Background()
	f := newOutcomesFixture(t)

	publishAt := time.Now().Add(-time.Hour)
	entry := f.committedEntry(t, ctx, "My Great Video Title", publishAt)
	video := f.syncedVideo(t, ctx, "yt-above", "My Great Video Title", publishAt)

	yt := &fake.Client{MetricsByVideoID: map[string]youtube.VideoMetrics{
		"yt-above": {YouTubeVideoID: "yt-above", Views: ptrInt64(500), MeasuredAt: time.Now()},
	}}
	a := newSyncActivities(f.st, yt)

	require.NoError(t, a.SyncOutcomes(ctx, f.ch.ID))

	m := f.singleMatchFor(t, ctx, video.ID)
	assert.Equal(t, store.MatchStateAuto, m.State)
	require.NotNil(t, m.ScheduleEntryID)
	assert.Equal(t, entry.ID, *m.ScheduleEntryID)
	assert.GreaterOrEqual(t, m.Confidence, MatchConfidenceThreshold)

	rows := predictionVsOutcomeRowsForChannel(t, ctx, f.db, f.ch.ID)
	require.Len(t, rows, 1, "an auto-matched, published, committed idea must appear in the comparison view")
	assert.Equal(t, "My Great Video Title", rows[0].IdeaTitle)
	require.NotNil(t, rows[0].Views)
	assert.Equal(t, int64(500), *rows[0].Views)
}

// ── below-threshold: pending, no authoritative link, absent from the view ──

func TestSyncOutcomes_BelowThreshold_OnePendingMatch_NoLinkNotInPredictionVsOutcome(t *testing.T) {
	ctx := context.Background()
	f := newOutcomesFixture(t)

	publishAt := time.Now().Add(-time.Hour)
	f.committedEntry(t, ctx, "Totally Unrelated Idea Title", publishAt.Add(60*24*time.Hour)) // wildly different title AND date
	video := f.syncedVideo(t, ctx, "yt-below", "Something Else Entirely", publishAt)

	yt := &fake.Client{MetricsByVideoID: map[string]youtube.VideoMetrics{
		"yt-below": {YouTubeVideoID: "yt-below", Views: ptrInt64(10), MeasuredAt: time.Now()},
	}}
	a := newSyncActivities(f.st, yt)

	require.NoError(t, a.SyncOutcomes(ctx, f.ch.ID))

	m := f.singleMatchFor(t, ctx, video.ID)
	assert.Equal(t, store.MatchStatePending, m.State, "a below-threshold match must be queued pending, never auto-linked (FR23)")
	assert.Less(t, m.Confidence, MatchConfidenceThreshold)

	rows := predictionVsOutcomeRowsForChannel(t, ctx, f.db, f.ch.ID)
	assert.Empty(t, rows, "a pending match's schedule_entry_id must not be treated as authoritative -- it must not appear in v_prediction_vs_outcome")
}

// ── no plausible candidate at all: still pending, never auto-linked ────────

func TestSyncOutcomes_NoCandidates_StillPendingNeverAutoLinked(t *testing.T) {
	ctx := context.Background()
	f := newOutcomesFixture(t)

	publishAt := time.Now().Add(-time.Hour)
	video := f.syncedVideo(t, ctx, "yt-no-candidates", "An Orphan Video", publishAt)

	yt := &fake.Client{MetricsByVideoID: map[string]youtube.VideoMetrics{
		"yt-no-candidates": {YouTubeVideoID: "yt-no-candidates", Views: ptrInt64(1), MeasuredAt: time.Now()},
	}}
	a := newSyncActivities(f.st, yt)

	require.NoError(t, a.SyncOutcomes(ctx, f.ch.ID))

	m := f.singleMatchFor(t, ctx, video.ID)
	assert.Equal(t, store.MatchStatePending, m.State, "no committed schedule entry at all must still queue pending, never fabricate an auto-link")
	assert.Nil(t, m.ScheduleEntryID)
	assert.Equal(t, 0.0, m.Confidence)
}

// ── issue #1652: a candidate committed AFTER the video already synced with
// no candidates must still get matched on the next cycle, not stay stuck
// on the first cycle's "no candidate at all" placeholder forever ─────────

func TestSyncOutcomes_CandidateCommittedAfterFirstSync_SecondCycleMatchesInPlace(t *testing.T) {
	ctx := context.Background()
	f := newOutcomesFixture(t)

	publishAt := time.Now().Add(-time.Hour)
	// The video syncs BEFORE any committed schedule_entry exists for the
	// Channel (the backdated/historical scenario issue #1652 reproduced) --
	// first SyncOutcomes call must still record the documented "no
	// plausible candidate at all" pending placeholder (confidence 0, nil
	// schedule_entry_id).
	video := f.syncedVideo(t, ctx, "yt-late-candidate", "Exact Title Match", publishAt)

	yt := &fake.Client{MetricsByVideoID: map[string]youtube.VideoMetrics{
		"yt-late-candidate": {YouTubeVideoID: "yt-late-candidate", Views: ptrInt64(7), MeasuredAt: time.Now()},
	}}
	a := newSyncActivities(f.st, yt)

	require.NoError(t, a.SyncOutcomes(ctx, f.ch.ID))
	first := f.singleMatchFor(t, ctx, video.ID)
	firstID := first.ID
	assert.Equal(t, store.MatchStatePending, first.State)
	assert.Nil(t, first.ScheduleEntryID)
	assert.Equal(t, 0.0, first.Confidence)

	// Now the matching schedule_entry gets committed (e.g. a human commits
	// a backdated draft against this exact already-synced video). A second
	// sync cycle must NOT skip this video just because it already carries a
	// video_schedule_match row -- that row is the no-candidate placeholder,
	// not a settled match.
	entry := f.committedEntry(t, ctx, "Exact Title Match", publishAt)
	require.NoError(t, a.SyncOutcomes(ctx, f.ch.ID))

	second := f.singleMatchFor(t, ctx, video.ID)
	assert.Equal(t, firstID, second.ID, "the placeholder row must be updated in place, not duplicated")
	assert.Equal(t, store.MatchStateAuto, second.State, "exact title+date match must auto-link once the candidate exists (FR22)")
	require.NotNil(t, second.ScheduleEntryID)
	assert.Equal(t, entry.ID, *second.ScheduleEntryID)
	assert.GreaterOrEqual(t, second.Confidence, MatchConfidenceThreshold)

	var matchCount int
	require.NoError(t, f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM video_schedule_match WHERE synced_video_id = $1`, video.ID).Scan(&matchCount))
	assert.Equal(t, 1, matchCount, "must never leave a stale duplicate row behind")
}

// ── double run: no duplicate matches, no duplicate metric rows ─────────────

func TestSyncOutcomes_DoubleRun_NoDuplicateMatchesNoDuplicateMetricRows(t *testing.T) {
	ctx := context.Background()
	f := newOutcomesFixture(t)

	publishAt := time.Now().Add(-time.Hour)
	f.committedEntry(t, ctx, "Stable Title", publishAt)
	video := f.syncedVideo(t, ctx, "yt-stable", "Stable Title", publishAt)

	measuredAt := time.Now()
	yt := &fake.Client{MetricsByVideoID: map[string]youtube.VideoMetrics{
		"yt-stable": {YouTubeVideoID: "yt-stable", Views: ptrInt64(100), MeasuredAt: measuredAt},
	}}
	a := newSyncActivities(f.st, yt)

	require.NoError(t, a.SyncOutcomes(ctx, f.ch.ID))
	require.NoError(t, a.SyncOutcomes(ctx, f.ch.ID))

	var matchCount int
	require.NoError(t, f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM video_schedule_match WHERE synced_video_id = $1`, video.ID).Scan(&matchCount))
	assert.Equal(t, 1, matchCount, "running the activity twice must not create a second match for the same video -- HasMatch gates it (idempotent)")

	var metricsCount int
	require.NoError(t, f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM video_metrics WHERE synced_video_id = $1`, video.ID).Scan(&metricsCount))
	assert.Equal(t, 1, metricsCount, "re-measuring the same instant twice must upsert to one row, not duplicate")
}

// ── metrics accumulate across cycles at genuinely different measured_at ────

func TestSyncOutcomes_TwoRunsDifferentMeasuredAt_MetricsAccumulate_ViewReadsLatest(t *testing.T) {
	ctx := context.Background()
	f := newOutcomesFixture(t)

	publishAt := time.Now().Add(-time.Hour)
	f.committedEntry(t, ctx, "Accumulating Title", publishAt)
	video := f.syncedVideo(t, ctx, "yt-accum", "Accumulating Title", publishAt)

	firstMeasuredAt := time.Now().Add(-time.Hour)
	yt := &fake.Client{MetricsByVideoID: map[string]youtube.VideoMetrics{
		"yt-accum": {YouTubeVideoID: "yt-accum", Views: ptrInt64(100), MeasuredAt: firstMeasuredAt},
	}}
	a := newSyncActivities(f.st, yt)
	require.NoError(t, a.SyncOutcomes(ctx, f.ch.ID))

	secondMeasuredAt := time.Now()
	yt.MetricsByVideoID["yt-accum"] = youtube.VideoMetrics{YouTubeVideoID: "yt-accum", Views: ptrInt64(999), MeasuredAt: secondMeasuredAt}
	require.NoError(t, a.SyncOutcomes(ctx, f.ch.ID))

	var metricsCount int
	require.NoError(t, f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM video_metrics WHERE synced_video_id = $1`, video.ID).Scan(&metricsCount))
	assert.Equal(t, 2, metricsCount, "two genuinely distinct measured_at instants must produce two rows, an append not an overwrite")

	latest, err := f.st.Sync().LatestMetricsFor(ctx, video.ID)
	require.NoError(t, err)
	require.NotNil(t, latest)
	require.NotNil(t, latest.Views)
	assert.Equal(t, int64(999), *latest.Views, "LatestMetricsFor must read the latest measured_at, not the first")

	rows := predictionVsOutcomeRowsForChannel(t, ctx, f.db, f.ch.ID)
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].Views)
	assert.Equal(t, int64(999), *rows[0].Views, "v_prediction_vs_outcome must also read the latest metrics row")
}

// ── ErrRevoked mid-run: needs-reauth, retained data, non-retryable ─────────

func TestSyncOutcomes_ErrRevoked_MarksNeedsReauthRetainsDataNonRetryable(t *testing.T) {
	ctx := context.Background()
	f := newOutcomesFixture(t)

	publishAt := time.Now().Add(-time.Hour)
	video := f.syncedVideo(t, ctx, "yt-revoked", "A Video", publishAt)

	// Seed one existing video_metrics row directly, so this test proves
	// ErrRevoked leaves already-synced data alone, not merely that it fails
	// to add new rows (mirrors video_sync_test.go's ErrRevoked test).
	require.NoError(t, f.st.Sync().UpsertMetrics(ctx, []store.VideoMetrics{{
		SyncedVideoID: video.ID, Views: ptrInt64(42), MeasuredAt: time.Now().Add(-24 * time.Hour),
	}}))

	yt := &fake.Client{Err: youtube.ErrRevoked}
	a := newSyncActivities(f.st, yt)

	err := a.SyncOutcomes(ctx, f.ch.ID)
	require.Error(t, err)

	var appErr *temporal.ApplicationError
	require.True(t, errors.As(err, &appErr), "ErrRevoked must surface as a temporal.ApplicationError, got %T: %v", err, err)
	assert.True(t, appErr.NonRetryable())
	assert.Equal(t, RevokedErrorType, appErr.Type())

	got, getErr := f.st.Channels().GetByID(ctx, f.ch.ID)
	require.NoError(t, getErr)
	assert.Equal(t, store.ConnectionStateNeedsReauth, got.ConnectionState)

	var metricsCount int
	require.NoError(t, f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM video_metrics WHERE synced_video_id = $1`, video.ID).Scan(&metricsCount))
	assert.Equal(t, 1, metricsCount, "the pre-existing metrics row must be retained, and no partial write attempted, on a mid-cycle revocation")

	var matchCount int
	require.NoError(t, f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM video_schedule_match WHERE synced_video_id = $1`, video.ID).Scan(&matchCount))
	assert.Equal(t, 0, matchCount, "matching never runs once metrics sync has failed this cycle")
}
