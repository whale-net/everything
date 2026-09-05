//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it -- mirrors video_sync_test.go's pattern exactly: spin up a throwaway
// Postgres via dbtest, apply the real embedded migrations, drive
// SyncOutcomes (outcomes.go) against it with a youtube/fake.Client
// standing in for the real YouTube Analytics API, and assert against the
// real store.MatchStore/store.SyncStore/store.VideoScriptStore rows --
// re-anchored onto `video_script` by FR43/#1829 (see matching.go and
// store/match.go for the production-code side of the re-anchor). FR44's
// re-anchor of v_prediction_vs_outcome/get_prediction_vs_outcome onto
// video_script is #1830's scope, not this file's -- so these tests assert
// against video_schedule_match/video_script rows directly rather than
// through that view.
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
// with a live creator, ready for a greenlit video_script (matching
// candidate) and a published synced_video (the thing to match against it).
// Deliberately creates its own Channel/Person (rather than reusing
// setupSyncChannel, which does not hand the creator id back) so
// greenlitScript never needs to re-derive it via a role lookup.
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

// proposedScript creates an Idea -> viable Verdict -> Strategy chain on
// f.ch and proposes a video_script against it (FR36), left in status
// 'proposed' -- the starting point every video_script fixture below builds
// on, and itself the fixture for the "never a candidate" proposed case.
func (f *outcomesFixture) proposedScript(t *testing.T, ctx context.Context, title string, targetPublishDate *time.Time) store.VideoScript {
	t.Helper()

	idea, err := f.st.Ideas().Create(ctx, f.ch.ID, title, f.creator.ID)
	require.NoError(t, err)
	verdict, err := f.st.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "viable for outcomes test", AuthorPersonID: f.creator.ID,
	})
	require.NoError(t, err)
	strategy, err := f.st.Strategies().Save(ctx, store.SaveStrategyInput{
		ChannelID: f.ch.ID, Title: title + " Strategy", Cadence: store.CadenceWeekly, Active: true,
		VerdictIDs: []uuid.UUID{verdict.ID}, CreatedByPersonID: f.creator.ID,
	})
	require.NoError(t, err)

	script, err := f.st.VideoScripts().Propose(ctx, store.ProposeVideoScriptInput{
		ChannelID: f.ch.ID, VerdictID: verdict.ID, StrategyID: strategy.ID,
		Title: title, ScriptText: "script text for " + title, TargetPublishDate: targetPublishDate,
		CreatedByPersonID: f.creator.ID,
	})
	require.NoError(t, err)
	return script
}

// greenlitScript builds on proposedScript and immediately greenlights it
// (FR37) -- the matcher's candidate pool (MatchStore.ListCandidates only
// returns `greenlit` video_script rows, FR43).
func (f *outcomesFixture) greenlitScript(t *testing.T, ctx context.Context, title string, targetPublishDate *time.Time) store.VideoScript {
	t.Helper()

	script := f.proposedScript(t, ctx, title, targetPublishDate)
	require.NoError(t, f.st.VideoScripts().Greenlight(ctx, script.ID, f.creator.ID))
	got, err := f.st.VideoScripts().GetByID(ctx, script.ID)
	require.NoError(t, err)
	return got
}

// deniedScript builds on proposedScript and immediately denies it (FR38) --
// the fixture for the "never a candidate" denied case.
func (f *outcomesFixture) deniedScript(t *testing.T, ctx context.Context, title string) store.VideoScript {
	t.Helper()

	script := f.proposedScript(t, ctx, title, nil)
	require.NoError(t, f.st.VideoScripts().Deny(ctx, script.ID, f.creator.ID))
	got, err := f.st.VideoScripts().GetByID(ctx, script.ID)
	require.NoError(t, err)
	return got
}

// archivedScript builds a greenlit script and immediately archives it
// (FR39 -- no live match yet, so the freeze predicate never blocks this)
// -- the fixture for the "never a candidate" archived case.
func (f *outcomesFixture) archivedScript(t *testing.T, ctx context.Context, title string) store.VideoScript {
	t.Helper()

	script := f.greenlitScript(t, ctx, title, nil)
	require.NoError(t, f.st.VideoScripts().Archive(ctx, script.ID, f.creator.ID))
	got, err := f.st.VideoScripts().GetByID(ctx, script.ID)
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

func ptrInt64(v int64) *int64 { return &v }

// ── metrics upsert + auto match on a greenlit video_script ─────────────────

func TestSyncOutcomes_AboveThreshold_GreenlitScript_AutoMatch(t *testing.T) {
	ctx := context.Background()
	f := newOutcomesFixture(t)

	publishAt := time.Now().Add(-time.Hour)
	script := f.greenlitScript(t, ctx, "My Great Video Title", &publishAt)
	video := f.syncedVideo(t, ctx, "yt-above", "My Great Video Title", publishAt)

	yt := &fake.Client{MetricsByVideoID: map[string]youtube.VideoMetrics{
		"yt-above": {YouTubeVideoID: "yt-above", Views: ptrInt64(500), MeasuredAt: time.Now()},
	}}
	a := newSyncActivities(f.st, yt)

	require.NoError(t, a.SyncOutcomes(ctx, f.ch.ID))

	m := f.singleMatchFor(t, ctx, video.ID)
	assert.Equal(t, store.MatchStateAuto, m.State)
	require.NotNil(t, m.VideoScriptID)
	assert.Equal(t, script.ID, *m.VideoScriptID)
	assert.GreaterOrEqual(t, m.Confidence, MatchConfidenceThreshold)
}

// ── below-threshold: pending, not auto-linked ───────────────────────────────

func TestSyncOutcomes_BelowThreshold_GreenlitScript_PendingNotAutoLinked(t *testing.T) {
	ctx := context.Background()
	f := newOutcomesFixture(t)

	publishAt := time.Now().Add(-time.Hour)
	farOff := publishAt.Add(60 * 24 * time.Hour)
	f.greenlitScript(t, ctx, "Totally Unrelated Idea Title", &farOff) // wildly different title AND date
	video := f.syncedVideo(t, ctx, "yt-below", "Something Else Entirely", publishAt)

	yt := &fake.Client{MetricsByVideoID: map[string]youtube.VideoMetrics{
		"yt-below": {YouTubeVideoID: "yt-below", Views: ptrInt64(10), MeasuredAt: time.Now()},
	}}
	a := newSyncActivities(f.st, yt)

	require.NoError(t, a.SyncOutcomes(ctx, f.ch.ID))

	m := f.singleMatchFor(t, ctx, video.ID)
	assert.Equal(t, store.MatchStatePending, m.State, "a below-threshold match must be queued pending, never auto-linked (FR23)")
	assert.Less(t, m.Confidence, MatchConfidenceThreshold)
}

// ── proposed/denied/archived scripts are NEVER offered as candidates ───────

func TestSyncOutcomes_OnlyNonGreenlitScriptsExist_PendingWithNilVideoScriptID(t *testing.T) {
	ctx := context.Background()
	f := newOutcomesFixture(t)

	publishAt := time.Now().Add(-time.Hour)
	// Every one of these has a title/date that would otherwise score above
	// threshold against the video below -- if ListCandidates leaked any of
	// them, this test would auto-link instead of staying pending.
	f.proposedScript(t, ctx, "Exact Title Match", &publishAt)
	f.deniedScript(t, ctx, "Exact Title Match")
	f.archivedScript(t, ctx, "Exact Title Match")
	video := f.syncedVideo(t, ctx, "yt-non-greenlit", "Exact Title Match", publishAt)

	yt := &fake.Client{MetricsByVideoID: map[string]youtube.VideoMetrics{
		"yt-non-greenlit": {YouTubeVideoID: "yt-non-greenlit", Views: ptrInt64(1), MeasuredAt: time.Now()},
	}}
	a := newSyncActivities(f.st, yt)

	require.NoError(t, a.SyncOutcomes(ctx, f.ch.ID))

	m := f.singleMatchFor(t, ctx, video.ID)
	assert.Equal(t, store.MatchStatePending, m.State, "proposed/denied/archived scripts must never be offered as candidates, however good a title/date match they'd otherwise be (FR43)")
	assert.Nil(t, m.VideoScriptID)
	assert.Equal(t, 0.0, m.Confidence)
}

// ── issue #1652 (re-anchored): a candidate greenlit AFTER the video already
// synced with no candidates must still get matched on the next cycle, not
// stay stuck on the first cycle's "no candidate at all" placeholder forever ──

func TestSyncOutcomes_ScriptGreenlitAfterFirstSync_SecondCycleMatchesInPlace(t *testing.T) {
	ctx := context.Background()
	f := newOutcomesFixture(t)

	publishAt := time.Now().Add(-time.Hour)
	// The video syncs BEFORE any greenlit video_script exists for the
	// Channel (the backdated/historical scenario issue #1652 reproduced) --
	// first SyncOutcomes call must still record the documented "no
	// plausible candidate at all" pending placeholder (confidence 0, nil
	// video_script_id).
	video := f.syncedVideo(t, ctx, "yt-late-candidate", "Exact Title Match", publishAt)

	yt := &fake.Client{MetricsByVideoID: map[string]youtube.VideoMetrics{
		"yt-late-candidate": {YouTubeVideoID: "yt-late-candidate", Views: ptrInt64(7), MeasuredAt: time.Now()},
	}}
	a := newSyncActivities(f.st, yt)

	require.NoError(t, a.SyncOutcomes(ctx, f.ch.ID))
	first := f.singleMatchFor(t, ctx, video.ID)
	firstID := first.ID
	assert.Equal(t, store.MatchStatePending, first.State)
	assert.Nil(t, first.VideoScriptID)
	assert.Equal(t, 0.0, first.Confidence)

	// Now the matching video_script gets greenlit (e.g. a human greenlights
	// a backdated proposal against this exact already-synced video). A
	// second sync cycle must NOT skip this video just because it already
	// carries a video_schedule_match row -- that row is the no-candidate
	// placeholder, not a settled match.
	script := f.greenlitScript(t, ctx, "Exact Title Match", &publishAt)
	require.NoError(t, a.SyncOutcomes(ctx, f.ch.ID))

	second := f.singleMatchFor(t, ctx, video.ID)
	assert.Equal(t, firstID, second.ID, "the placeholder row must be updated in place, not duplicated")
	assert.Equal(t, store.MatchStateAuto, second.State, "exact title+date match must auto-link once the candidate exists (FR22)")
	require.NotNil(t, second.VideoScriptID)
	assert.Equal(t, script.ID, *second.VideoScriptID)
	assert.GreaterOrEqual(t, second.Confidence, MatchConfidenceThreshold)

	var matchCount int
	require.NoError(t, f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM video_schedule_match WHERE synced_video_id = $1`, video.ID).Scan(&matchCount))
	assert.Equal(t, 1, matchCount, "must never leave a stale duplicate row behind")
}

// ── a video with an existing settled match is skipped (HasMatch) ───────────

func TestSyncOutcomes_ExistingSettledMatch_SkippedNotRescored(t *testing.T) {
	ctx := context.Background()
	f := newOutcomesFixture(t)

	publishAt := time.Now().Add(-time.Hour)
	first := f.greenlitScript(t, ctx, "Stable Title", &publishAt)
	video := f.syncedVideo(t, ctx, "yt-settled", "Stable Title", publishAt)

	yt := &fake.Client{MetricsByVideoID: map[string]youtube.VideoMetrics{
		"yt-settled": {YouTubeVideoID: "yt-settled", Views: ptrInt64(100), MeasuredAt: time.Now()},
	}}
	a := newSyncActivities(f.st, yt)
	require.NoError(t, a.SyncOutcomes(ctx, f.ch.ID))

	settled := f.singleMatchFor(t, ctx, video.ID)
	require.Equal(t, store.MatchStateAuto, settled.State)
	require.NotNil(t, settled.VideoScriptID)
	require.Equal(t, first.ID, *settled.VideoScriptID)

	// A second, even better-matching greenlit script now exists, but the
	// video already has a settled (auto) match -- HasMatch must gate it out
	// entirely, leaving the original match untouched.
	f.greenlitScript(t, ctx, "Stable Title", &publishAt)
	require.NoError(t, a.SyncOutcomes(ctx, f.ch.ID))

	after := f.singleMatchFor(t, ctx, video.ID)
	assert.Equal(t, settled.ID, after.ID)
	assert.Equal(t, settled.State, after.State)
	require.NotNil(t, after.VideoScriptID)
	assert.Equal(t, first.ID, *after.VideoScriptID, "a settled match must never be re-scored or re-linked to a different candidate")

	var matchCount int
	require.NoError(t, f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM video_schedule_match WHERE synced_video_id = $1`, video.ID).Scan(&matchCount))
	assert.Equal(t, 1, matchCount)
}

// ── double run: no duplicate matches, no duplicate metric rows ─────────────

func TestSyncOutcomes_DoubleRun_NoDuplicateMatchesNoDuplicateMetricRows(t *testing.T) {
	ctx := context.Background()
	f := newOutcomesFixture(t)

	publishAt := time.Now().Add(-time.Hour)
	f.greenlitScript(t, ctx, "Stable Title", &publishAt)
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

func TestSyncOutcomes_TwoRunsDifferentMeasuredAt_MetricsAccumulate_LatestIsRead(t *testing.T) {
	ctx := context.Background()
	f := newOutcomesFixture(t)

	publishAt := time.Now().Add(-time.Hour)
	f.greenlitScript(t, ctx, "Accumulating Title", &publishAt)
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
