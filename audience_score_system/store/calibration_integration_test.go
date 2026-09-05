//go:build integration

// calibration_integration_test.go covers CalibrationStore.MonthlyTrend
// (C14 / FR3 / FR4 / FR5 / FR7, issue #1884): FR3's calibration-candidate
// filter (viable verdict, published, synced metrics), FR4's
// meets-or-exceeds/NULL-is-a-miss classification against a caller-supplied
// bar with no stored history, FR5's chronological calendar-month
// bucketing, and FR7's backward-paging truncation semantics. Same
// package/build tag/harness as outcome_bar_integration_test.go --
// newStore/setupChannel/ptrTime/ptrInt64 (store_integration_test.go) and
// greenlitVideoScript/proposedVideoScript (match_integration_test.go) are
// reused directly.
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/libs/go/dbtest"
)

// viewsBar is a caller-supplied OutcomeBar for MonthlyTrend -- ID/
// ChannelID/UpdatedAt/UpdatedByPersonID are never read by MonthlyTrend
// (it does no authz and does not touch `outcome_bar` itself, per
// calibration.go's doc comment), so only MetricName/ThresholdValue matter
// here.
func viewsBar(threshold float64) store.OutcomeBar {
	return store.OutcomeBar{MetricName: store.OutcomeBarMetricViews, ThresholdValue: threshold}
}

// publishAndMatch publishes a synced video for script, confirms a match
// to it, and records a video_metrics snapshot carrying views (nil is a
// legitimate "no views value recorded" snapshot -- FR4's NULL-is-a-miss
// case). Returns the synced_video id.
func publishAndMatch(t *testing.T, ctx context.Context, s *store.Store, db *dbtest.Postgres, ch store.Channel, script store.VideoScript, publishedAt time.Time, views *int64) uuid.UUID {
	t.Helper()

	ytID := "yt-" + uuid.NewString()
	require.NoError(t, s.Sync().UpsertVideos(ctx, ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: ytID, Title: script.Title,
		PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: ptrTime(publishedAt), LastSyncedAt: time.Now(),
	}}))
	var syncedID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT id FROM synced_video WHERE youtube_video_id = $1`, ytID).Scan(&syncedID))

	require.NoError(t, s.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: syncedID, VideoScriptID: &script.ID, Confidence: 0.9, State: store.MatchStateConfirmed,
	}))
	require.NoError(t, s.Sync().UpsertMetrics(ctx, []store.VideoMetrics{{
		SyncedVideoID: syncedID, Views: views, MeasuredAt: time.Now(),
	}}))
	return syncedID
}

// publishAndMatchNoMetrics is publishAndMatch without the video_metrics
// row -- FR3c's "no synced metrics yet" exclusion case (test 1(e)):
// predictionOutcomeJoin's inner LATERAL join to video_metrics drops such a
// row before the calibration query's own WHERE ever runs.
func publishAndMatchNoMetrics(t *testing.T, ctx context.Context, s *store.Store, db *dbtest.Postgres, ch store.Channel, script store.VideoScript, publishedAt time.Time) {
	t.Helper()

	ytID := "yt-" + uuid.NewString()
	require.NoError(t, s.Sync().UpsertVideos(ctx, ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: ytID, Title: script.Title,
		PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: ptrTime(publishedAt), LastSyncedAt: time.Now(),
	}}))
	var syncedID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT id FROM synced_video WHERE youtube_video_id = $1`, ytID).Scan(&syncedID))

	require.NoError(t, s.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: syncedID, VideoScriptID: &script.ID, Confidence: 0.9, State: store.MatchStateConfirmed,
	}))
}

// calibrationCandidate builds a full FR3 calibration candidate on ch: a
// viable-verdict-bound greenlit video_script (greenlitVideoScript,
// match_integration_test.go), a confirmed match to a synced video
// published at publishedAt, and a video_metrics snapshot carrying views.
func calibrationCandidate(t *testing.T, ctx context.Context, s *store.Store, db *dbtest.Postgres, ch store.Channel, creator store.Person, title string, publishedAt time.Time, views *int64) store.VideoScript {
	t.Helper()

	script := greenlitVideoScript(t, ctx, s, ch, creator, title)
	publishAndMatch(t, ctx, s, db, ch, script, publishedAt, views)
	return script
}

// anyStrategyID returns a Strategy id on ch built from its own always-
// viable anchor Idea/Verdict -- used by rawScriptOnNonViableVerdict below
// as the FK target for strategy_id, since StrategyStore.Save itself
// rejects a non-viable VerdictID (ErrStrategyVerdictNotViable,
// strategy.go) just as VideoScriptStore.Propose rejects one for
// verdict_id (ErrVerdictNotViable, FR36) -- a Strategy built from the
// non-viable verdict under test is therefore not an option either.
func anyStrategyID(t *testing.T, ctx context.Context, s *store.Store, ch store.Channel, creator store.Person) uuid.UUID {
	t.Helper()

	idea, err := s.Ideas().Create(ctx, ch.ID, "Strategy Anchor "+uuid.NewString(), creator.ID)
	require.NoError(t, err)
	v, err := s.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "anchor", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	st, err := s.Strategies().Save(ctx, store.SaveStrategyInput{
		ChannelID: ch.ID, Title: "Anchor Strategy " + uuid.NewString(), Active: true,
		VerdictIDs: []uuid.UUID{v.ID}, CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)
	return st.ID
}

// rawScriptOnNonViableVerdict builds an Idea with a verdict of value
// verdict (VerdictNotViable or VerdictNeedsMoreResearch) and a `greenlit`
// video_script bound directly to it -- via a raw SQL INSERT, not
// VideoScriptStore.Propose, which would reject it outright with
// ErrVerdictNotViable (FR36) precisely because a script can never be
// bound to a non-viable verdict version through the store API. This is
// the only way to construct the fixture FR3a's test needs: proof that the
// calibration query's own `vv.verdict = 'viable'` predicate is real
// defense-in-depth, not merely relying on VideoScriptStore's invariant
// holding elsewhere (the same "reach past the store API to assert on raw
// table state" pattern store_integration_test.go's view tests use).
func rawScriptOnNonViableVerdict(t *testing.T, ctx context.Context, s *store.Store, db *dbtest.Postgres, ch store.Channel, creator store.Person, title string, verdict store.VerdictValue, strategyID uuid.UUID) store.VideoScript {
	t.Helper()

	idea, err := s.Ideas().Create(ctx, ch.ID, title, creator.ID)
	require.NoError(t, err)
	v, err := s.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: verdict, Reasoning: title + " reasoning", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	var scriptID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO video_script (channel_id, idea_id, verdict_id, strategy_id, title, script_text, status, created_by_person_id)
		VALUES ($1, $2, $3, $4, $5, 'seed script text', 'greenlit', $6)
		RETURNING id
	`, ch.ID, idea.ID, v.ID, strategyID, title, creator.ID).Scan(&scriptID))

	got, err := s.VideoScripts().GetByID(ctx, scriptID)
	require.NoError(t, err)
	return got
}

func monthStart(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 12, 0, 0, 0, time.UTC)
}

// ── FR3 candidate filter (test 1) ───────────────────────────────────────────

func TestCalibrationStore_MonthlyTrend_FR3_OnlyTrueCandidateCounted(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	// (a) viable verdict -> greenlit script -> confirmed match -> published
	// video -> metrics: the only true candidate.
	calibrationCandidate(t, ctx, s, db, ch, creator, "Candidate A", monthStart(2024, time.March, 10), ptrInt64(5000))

	// (b) not-viable verdict with the same downstream chain: must never be
	// counted -- FR3a.
	anchorStrategy := anyStrategyID(t, ctx, s, ch, creator)
	notViableScript := rawScriptOnNonViableVerdict(t, ctx, s, db, ch, creator, "Not Viable B", store.VerdictNotViable, anchorStrategy)
	publishAndMatch(t, ctx, s, db, ch, notViableScript, monthStart(2024, time.March, 11), ptrInt64(5000))

	// (c) needs-more-research verdict, same shape as (b) -- FR3a.
	needsResearchScript := rawScriptOnNonViableVerdict(t, ctx, s, db, ch, creator, "Needs Research C", store.VerdictNeedsMoreResearch, anchorStrategy)
	publishAndMatch(t, ctx, s, db, ch, needsResearchScript, monthStart(2024, time.March, 12), ptrInt64(5000))

	// (d) viable verdict, but the script is left 'proposed' -- no match, no
	// publish -- must never be counted (FR3b, via predictionOutcomeJoin's
	// greenlit/archived-only script filter).
	_ = proposedVideoScript(t, ctx, s, ch, creator, "Proposed D")

	// (e) viable verdict -> published video with NO video_metrics row --
	// FR3c, already enforced by predictionOutcomeJoin's inner LATERAL join.
	scriptE := greenlitVideoScript(t, ctx, s, ch, creator, "No Metrics E")
	publishAndMatchNoMetrics(t, ctx, s, db, ch, scriptE, monthStart(2024, time.March, 13))

	rows, truncated, err := s.Calibration().MonthlyTrend(ctx, ch.ID, viewsBar(1000), nil, nil, 0)
	require.NoError(t, err)
	assert.False(t, truncated)
	require.Len(t, rows, 1, "exactly one calendar-month bucket, from the single true candidate")
	assert.Equal(t, 1, rows[0].Candidates, "only fixture (a) is a calibration candidate")
	assert.Equal(t, 1, rows[0].Calibrated)
	assert.Equal(t, 0, rows[0].Miscalibrated)
}

// ── FR4 classification (tests 2/3/4/5) ──────────────────────────────────────

func TestCalibrationStore_MonthlyTrend_FR4_ClassifiesAboveAndBelowThreshold(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	calibrationCandidate(t, ctx, s, db, ch, creator, "Above Threshold", monthStart(2024, time.May, 5), ptrInt64(2000))
	calibrationCandidate(t, ctx, s, db, ch, creator, "Below Threshold", monthStart(2024, time.May, 15), ptrInt64(500))

	rows, _, err := s.Calibration().MonthlyTrend(ctx, ch.ID, viewsBar(1000), nil, nil, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, 2, rows[0].Candidates)
	assert.Equal(t, 1, rows[0].Calibrated)
	assert.Equal(t, 1, rows[0].Miscalibrated)
	assert.InDelta(t, 0.5, rows[0].Rate, 1e-9)
}

func TestCalibrationStore_MonthlyTrend_FR4_ThresholdBoundary_EqualCountsAsCalibrated(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	calibrationCandidate(t, ctx, s, db, ch, creator, "Exactly At Threshold", monthStart(2024, time.June, 1), ptrInt64(1000))

	rows, _, err := s.Calibration().MonthlyTrend(ctx, ch.ID, viewsBar(1000), nil, nil, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, 1, rows[0].Candidates)
	assert.Equal(t, 1, rows[0].Calibrated, "FR4: a views count exactly equal to the threshold meets it, must count as calibrated")
	assert.Equal(t, 0, rows[0].Miscalibrated)
}

func TestCalibrationStore_MonthlyTrend_FR4_NullViewsCountsAsMiscalibrated(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	calibrationCandidate(t, ctx, s, db, ch, creator, "No Views Value", monthStart(2024, time.July, 1), nil)

	rows, _, err := s.Calibration().MonthlyTrend(ctx, ch.ID, viewsBar(0), nil, nil, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, 1, rows[0].Candidates)
	assert.Equal(t, 0, rows[0].Calibrated, "a NULL views value must never count as calibrated, even against a threshold of 0")
	assert.Equal(t, 1, rows[0].Miscalibrated)
}

func TestCalibrationStore_MonthlyTrend_FR4_NoStoredHistory_ReclassifiesOnRerunWithDifferentBar(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	calibrationCandidate(t, ctx, s, db, ch, creator, "Reclassified", monthStart(2024, time.August, 1), ptrInt64(1500))

	low, _, err := s.Calibration().MonthlyTrend(ctx, ch.ID, viewsBar(1000), nil, nil, 0)
	require.NoError(t, err)
	require.Len(t, low, 1)
	assert.Equal(t, 1, low[0].Calibrated, "1500 views clears a 1000 threshold")

	high, _, err := s.Calibration().MonthlyTrend(ctx, ch.ID, viewsBar(2000), nil, nil, 0)
	require.NoError(t, err)
	require.Len(t, high, 1)
	assert.Equal(t, 0, high[0].Calibrated, "the same seeded data must reclassify as miscalibrated against a higher threshold -- no backfill, no stored history")
	assert.Equal(t, 1, high[0].Candidates, "the candidate count itself does not change with the bar")
}

// ── FR5 bucketing (test 6) ───────────────────────────────────────────────────

func TestCalibrationStore_MonthlyTrend_FR5_BucketsByCalendarMonthChronologically(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	calibrationCandidate(t, ctx, s, db, ch, creator, "January", monthStart(2024, time.January, 10), ptrInt64(1000))
	calibrationCandidate(t, ctx, s, db, ch, creator, "February One", monthStart(2024, time.February, 5), ptrInt64(1000))
	calibrationCandidate(t, ctx, s, db, ch, creator, "February Two", monthStart(2024, time.February, 20), ptrInt64(1000))
	calibrationCandidate(t, ctx, s, db, ch, creator, "March", monthStart(2024, time.March, 1), ptrInt64(1000))

	rows, truncated, err := s.Calibration().MonthlyTrend(ctx, ch.ID, viewsBar(1000), nil, nil, 0)
	require.NoError(t, err)
	assert.False(t, truncated)
	require.Len(t, rows, 3, "January, February, March -- two February candidates collapse into one row")

	assert.True(t, rows[0].BucketStart.UTC().Equal(time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)))
	assert.True(t, rows[1].BucketStart.UTC().Equal(time.Date(2024, time.February, 1, 0, 0, 0, 0, time.UTC)))
	assert.True(t, rows[2].BucketStart.UTC().Equal(time.Date(2024, time.March, 1, 0, 0, 0, 0, time.UTC)))

	assert.Equal(t, 1, rows[0].Candidates)
	assert.Equal(t, 2, rows[1].Candidates, "the two February candidates must collapse into one row")
	assert.Equal(t, 1, rows[2].Candidates)
}

// ── since/before window (test 7) ─────────────────────────────────────────────

func TestCalibrationStore_MonthlyTrend_SinceBeforeNarrowTheWindow(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	calibrationCandidate(t, ctx, s, db, ch, creator, "January", monthStart(2024, time.January, 10), ptrInt64(1000))
	calibrationCandidate(t, ctx, s, db, ch, creator, "February", monthStart(2024, time.February, 10), ptrInt64(1000))
	calibrationCandidate(t, ctx, s, db, ch, creator, "March", monthStart(2024, time.March, 10), ptrInt64(1000))

	all, _, err := s.Calibration().MonthlyTrend(ctx, ch.ID, viewsBar(1000), nil, nil, 0)
	require.NoError(t, err)
	require.Len(t, all, 3, "nil/nil is unbounded on both sides")

	since := time.Date(2024, time.February, 1, 0, 0, 0, 0, time.UTC)
	before := time.Date(2024, time.March, 1, 0, 0, 0, 0, time.UTC)
	feb, _, err := s.Calibration().MonthlyTrend(ctx, ch.ID, viewsBar(1000), &since, &before, 0)
	require.NoError(t, err)
	require.Len(t, feb, 1, "since/before must narrow the window to just February")
	assert.True(t, feb[0].BucketStart.UTC().Equal(time.Date(2024, time.February, 1, 0, 0, 0, 0, time.UTC)))
}

// ── FR7 truncation (test 8) ──────────────────────────────────────────────────

func TestCalibrationStore_MonthlyTrend_FR7_LimitTruncatesToMostRecentButStaysChronological(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	months := []time.Month{time.January, time.February, time.March, time.April, time.May}
	for _, m := range months {
		calibrationCandidate(t, ctx, s, db, ch, creator, m.String(), monthStart(2024, m, 10), ptrInt64(1000))
	}

	limited, truncated, err := s.Calibration().MonthlyTrend(ctx, ch.ID, viewsBar(1000), nil, nil, 2)
	require.NoError(t, err)
	assert.True(t, truncated, "5 months exist, limit 2 must report truncation")
	require.Len(t, limited, 2)
	assert.True(t, limited[0].BucketStart.UTC().Equal(time.Date(2024, time.April, 1, 0, 0, 0, 0, time.UTC)), "the two MOST RECENT months, kept chronological between themselves")
	assert.True(t, limited[1].BucketStart.UTC().Equal(time.Date(2024, time.May, 1, 0, 0, 0, 0, time.UTC)))

	all, truncatedAll, err := s.Calibration().MonthlyTrend(ctx, ch.ID, viewsBar(1000), nil, nil, 10)
	require.NoError(t, err)
	assert.False(t, truncatedAll, "limit 10 exceeds the 5 available months")
	assert.Len(t, all, 5)

	unbounded, truncatedUnbounded, err := s.Calibration().MonthlyTrend(ctx, ch.ID, viewsBar(1000), nil, nil, 0)
	require.NoError(t, err)
	assert.False(t, truncatedUnbounded, "limit 0 is unbounded")
	assert.Len(t, unbounded, 5)
}

// ── Empty result (test 9) ────────────────────────────────────────────────────

func TestCalibrationStore_MonthlyTrend_NoCandidates_ReturnsEmptyNotError(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch, _ := setupChannel(t, ctx, s)

	rows, truncated, err := s.Calibration().MonthlyTrend(ctx, ch.ID, viewsBar(1000), nil, nil, 0)
	require.NoError(t, err)
	assert.False(t, truncated)
	assert.Empty(t, rows)
}

// ── Multi-tenant isolation (test 10) ─────────────────────────────────────────

func TestCalibrationStore_MonthlyTrend_MultiTenantIsolation(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch1, creator1 := setupChannel(t, ctx, s)
	ch2, creator2 := setupChannel(t, ctx, s)

	calibrationCandidate(t, ctx, s, db, ch1, creator1, "Channel 1 Candidate", monthStart(2024, time.September, 1), ptrInt64(1000))
	calibrationCandidate(t, ctx, s, db, ch2, creator2, "Channel 2 Candidate", monthStart(2024, time.September, 1), ptrInt64(1000))

	rows1, _, err := s.Calibration().MonthlyTrend(ctx, ch1.ID, viewsBar(1000), nil, nil, 0)
	require.NoError(t, err)
	require.Len(t, rows1, 1)
	assert.Equal(t, 1, rows1[0].Candidates, "ch1's bucket must not include ch2's candidate")

	rows2, _, err := s.Calibration().MonthlyTrend(ctx, ch2.ID, viewsBar(1000), nil, nil, 0)
	require.NoError(t, err)
	require.Len(t, rows2, 1)
	assert.Equal(t, 1, rows2[0].Candidates)
}

// ── Unsupported metric (test 11) ─────────────────────────────────────────────

func TestCalibrationStore_MonthlyTrend_UnsupportedMetric_ReturnsErrorNoRows(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch, _ := setupChannel(t, ctx, s)

	rows, truncated, err := s.Calibration().MonthlyTrend(ctx, ch.ID, store.OutcomeBar{MetricName: "ctr", ThresholdValue: 1}, nil, nil, 0)
	assert.ErrorIs(t, err, store.ErrUnsupportedOutcomeBarMetric)
	assert.Nil(t, rows)
	assert.False(t, truncated)
}

// ── LB3 regression guard (test 12) ───────────────────────────────────────────

// TestCalibrationStore_MonthlyTrend_LB3_StaysOnBoundVerdictVersionNotCurrent
// is the calibration-query counterpart of BrowseStore.PredictionVsOutcome's
// LB3 doc comment (browse.go): a video_script's verdict_id is a bound
// version, never a moving "current" pointer. Appending a newer, not-viable
// verdict version to the SAME idea after the candidate's greenlit script
// was already bound to the older viable version must not un-count it --
// the calibration query must never route through v_current_verdict.
func TestCalibrationStore_MonthlyTrend_LB3_StaysOnBoundVerdictVersionNotCurrent(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	script := greenlitVideoScript(t, ctx, s, ch, creator, "LB3 Candidate")
	publishAndMatch(t, ctx, s, db, ch, script, monthStart(2024, time.October, 1), ptrInt64(1000))

	// Append a newer, not-viable verdict version to the same idea -- the
	// script's verdict_id still points at v1, which was and remains
	// 'viable'; only the idea's CURRENT verdict has changed.
	_, err := s.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: script.IdeaID, Verdict: store.VerdictNotViable, Reasoning: "reassessed", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	rows, _, err := s.Calibration().MonthlyTrend(ctx, ch.ID, viewsBar(1000), nil, nil, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1, "the candidate must still be counted off its bound v1 verdict, not the idea's now-current v2")
	assert.Equal(t, 1, rows[0].Candidates)
	assert.Equal(t, 1, rows[0].Calibrated)
}
