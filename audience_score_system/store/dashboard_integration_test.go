//go:build integration

// dashboard_integration_test.go covers DashboardStore.ChannelActivity
// (issue #2038, FR23-FR26, C20): FR23's five entity-type counts over the
// trailing 24h/7d windows (including the load-bearing "newly-linked video"
// definition -- video_schedule_match.created_at, never resolved_at), FR24's
// outcome count + prior-period trend, FR26's zero-activity-Channel
// behavior, NFR3's bounded-round-trip guarantee, and multi-tenant scoping.
// Same package/build tag/harness as calibration_integration_test.go and
// match_integration_test.go -- newStore/setupChannel/ptrTime/ptrInt64/
// queryCounter/tracedStore (store_integration_test.go),
// setResearchNoteCreatedAt (store_integration_test.go), and
// greenlitVideoScript (match_integration_test.go) are reused directly.
package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/libs/go/dbtest"
)

// farPastFloor is far enough before any window this file exercises (the
// widest is the FR24 prior-7d window, 14 days) that backdating a fixture's
// support scaffolding (an Idea/Verdict built only to anchor a VideoScript
// under test) to now.Add(-farPastFloor) guarantees it never leaks into any
// window's count, regardless of which `now` a given test uses.
const farPastFloor = 100 * 24 * time.Hour

func setIdeaCreatedAt(t *testing.T, ctx context.Context, db *dbtest.Postgres, ideaID uuid.UUID, at time.Time) {
	t.Helper()
	_, err := db.Pool.Exec(ctx, `UPDATE idea SET created_at = $1 WHERE id = $2`, at, ideaID)
	require.NoError(t, err)
}

func setVerdictCreatedAt(t *testing.T, ctx context.Context, db *dbtest.Postgres, verdictID uuid.UUID, at time.Time) {
	t.Helper()
	_, err := db.Pool.Exec(ctx, `UPDATE viability_verdict SET created_at = $1 WHERE id = $2`, at, verdictID)
	require.NoError(t, err)
}

func setVideoScriptCreatedAt(t *testing.T, ctx context.Context, db *dbtest.Postgres, scriptID uuid.UUID, at time.Time) {
	t.Helper()
	_, err := db.Pool.Exec(ctx, `UPDATE video_script SET created_at = $1 WHERE id = $2`, at, scriptID)
	require.NoError(t, err)
}

func setMatchCreatedAt(t *testing.T, ctx context.Context, db *dbtest.Postgres, matchID uuid.UUID, at time.Time) {
	t.Helper()
	_, err := db.Pool.Exec(ctx, `UPDATE video_schedule_match SET created_at = $1 WHERE id = $2`, at, matchID)
	require.NoError(t, err)
}

// verdictAt builds a viable Verdict on a fresh Idea, backdating the Idea's
// own created_at to now.Add(-farPastFloor) (so it never contributes to a
// FR23 Ideas count in the same test) and the Verdict's created_at to at.
func verdictAt(t *testing.T, ctx context.Context, s *store.Store, db *dbtest.Postgres, ch store.Channel, creator store.Person, title string, now, at time.Time) store.Verdict {
	t.Helper()

	idea, err := s.Ideas().Create(ctx, ch.ID, title, creator.ID)
	require.NoError(t, err)
	setIdeaCreatedAt(t, ctx, db, idea.ID, now.Add(-farPastFloor))

	v, err := s.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: title + " reasoning", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	setVerdictCreatedAt(t, ctx, db, v.ID, at)
	return v
}

// scriptAt builds a full greenlit VideoScript (via greenlitVideoScript,
// match_integration_test.go), backdating its supporting Idea/Verdict to
// now.Add(-farPastFloor) so they never contribute to the FR23 Ideas/
// Verdicts counts in the same test, then backdates only the VideoScript's
// own created_at to at -- isolating the VideoScripts count fixture.
func scriptAt(t *testing.T, ctx context.Context, s *store.Store, db *dbtest.Postgres, ch store.Channel, creator store.Person, title string, now, at time.Time) store.VideoScript {
	t.Helper()

	script := greenlitVideoScript(t, ctx, s, ch, creator, title)
	farPast := now.Add(-farPastFloor)
	setIdeaCreatedAt(t, ctx, db, script.IdeaID, farPast)
	setVerdictCreatedAt(t, ctx, db, script.VerdictID, farPast)
	setVideoScriptCreatedAt(t, ctx, db, script.ID, at)
	return script
}

// matchAt records a MatchStatePending video_schedule_match against a fresh
// synced_video on ch, then backdates the match's own created_at to at.
// Pending (rather than auto/confirmed) so callers may also exercise
// MatchStore.Resolve against the returned id -- FR23's linked-videos count
// itself is state-agnostic (activityCountsQuery reads only created_at).
func matchAt(t *testing.T, ctx context.Context, s *store.Store, db *dbtest.Postgres, ch store.Channel, label string, at time.Time) uuid.UUID {
	t.Helper()

	ytID := "yt-" + label + "-" + uuid.NewString()
	require.NoError(t, s.Sync().UpsertVideos(ctx, ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: ytID, Title: label,
		PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: ptrTime(time.Now()), LastSyncedAt: time.Now(),
	}}))
	var syncedID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT id FROM synced_video WHERE youtube_video_id = $1`, ytID).Scan(&syncedID))

	require.NoError(t, s.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: syncedID, Confidence: 0.9, State: store.MatchStatePending,
	}))
	var matchID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT id FROM video_schedule_match WHERE synced_video_id = $1`, syncedID).Scan(&matchID))
	setMatchCreatedAt(t, ctx, db, matchID, at)
	return matchID
}

// publishedVideoAt upserts a published synced_video on ch with the given
// published_at, returning its id -- FR24's outcomes fixture builder. No
// video_metrics row is attached; callers add one via Sync().UpsertMetrics
// when the fixture needs to count as an outcome (FR24: published alone is
// not enough).
func publishedVideoAt(t *testing.T, ctx context.Context, s *store.Store, db *dbtest.Postgres, ch store.Channel, label string, publishedAt time.Time) uuid.UUID {
	t.Helper()

	ytID := "yt-" + label + "-" + uuid.NewString()
	require.NoError(t, s.Sync().UpsertVideos(ctx, ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: ytID, Title: label,
		PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: ptrTime(publishedAt), LastSyncedAt: time.Now(),
	}}))
	var syncedID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT id FROM synced_video WHERE youtube_video_id = $1`, ytID).Scan(&syncedID))
	return syncedID
}

// ── FR23: per-entity-type counts across the 24h/7d windows ─────────────────

func TestDashboardStore_ChannelActivity_FR23_CountsEachEntityTypeAcrossWindows(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	now := time.Now().UTC()
	within24h := now.Add(-1 * time.Hour)
	within7dNot24h := now.Add(-30 * time.Hour)
	outside7d := now.Add(-8 * 24 * time.Hour)

	// Research notes.
	n1, err := s.Research().SaveNote(ctx, store.SaveNoteInput{ChannelID: ch.ID, ThreadTitle: "N1", Text: "n1", AuthorPersonID: creator.ID})
	require.NoError(t, err)
	setResearchNoteCreatedAt(t, ctx, db, n1.ID, within24h)
	n2, err := s.Research().SaveNote(ctx, store.SaveNoteInput{ChannelID: ch.ID, ThreadTitle: "N2", Text: "n2", AuthorPersonID: creator.ID})
	require.NoError(t, err)
	setResearchNoteCreatedAt(t, ctx, db, n2.ID, within7dNot24h)
	n3, err := s.Research().SaveNote(ctx, store.SaveNoteInput{ChannelID: ch.ID, ThreadTitle: "N3", Text: "n3", AuthorPersonID: creator.ID})
	require.NoError(t, err)
	setResearchNoteCreatedAt(t, ctx, db, n3.ID, outside7d)

	// Ideas.
	i1, err := s.Ideas().Create(ctx, ch.ID, "I1", creator.ID)
	require.NoError(t, err)
	setIdeaCreatedAt(t, ctx, db, i1.ID, within24h)
	i2, err := s.Ideas().Create(ctx, ch.ID, "I2", creator.ID)
	require.NoError(t, err)
	setIdeaCreatedAt(t, ctx, db, i2.ID, within7dNot24h)
	i3, err := s.Ideas().Create(ctx, ch.ID, "I3", creator.ID)
	require.NoError(t, err)
	setIdeaCreatedAt(t, ctx, db, i3.ID, outside7d)

	// Verdicts -- own Ideas backdated far outside every window so they
	// don't pollute the Ideas assertions above.
	verdictAt(t, ctx, s, db, ch, creator, "V1", now, within24h)
	verdictAt(t, ctx, s, db, ch, creator, "V2", now, within7dNot24h)
	verdictAt(t, ctx, s, db, ch, creator, "V3", now, outside7d)

	// Video scripts -- own Idea/Verdict/Strategy backdated far outside
	// every window so they don't pollute the Ideas/Verdicts assertions.
	scriptAt(t, ctx, s, db, ch, creator, "S1", now, within24h)
	scriptAt(t, ctx, s, db, ch, creator, "S2", now, within7dNot24h)
	scriptAt(t, ctx, s, db, ch, creator, "S3", now, outside7d)

	// Newly-linked videos (video_schedule_match.created_at).
	matchAt(t, ctx, s, db, ch, "M1", within24h)
	matchAt(t, ctx, s, db, ch, "M2", within7dNot24h)
	matchAt(t, ctx, s, db, ch, "M3", outside7d)

	activity, err := s.Dashboard().ChannelActivity(ctx, ch.ID, now)
	require.NoError(t, err)

	assert.Equal(t, 1, activity.Last24h.ResearchNotes, "only the 1h-old note is inside the 24h window")
	assert.Equal(t, 2, activity.Last7d.ResearchNotes, "the 1h and 30h-old notes are inside the 7d window; the 8d-old one is not")
	assert.Equal(t, 1, activity.Last24h.Ideas)
	assert.Equal(t, 2, activity.Last7d.Ideas)
	assert.Equal(t, 1, activity.Last24h.Verdicts)
	assert.Equal(t, 2, activity.Last7d.Verdicts)
	assert.Equal(t, 1, activity.Last24h.VideoScripts)
	assert.Equal(t, 2, activity.Last7d.VideoScripts)
	assert.Equal(t, 1, activity.Last24h.LinkedVideos)
	assert.Equal(t, 2, activity.Last7d.LinkedVideos)
}

// TestDashboardStore_ChannelActivity_FR23_24hBoundaryIsInclusive proves the
// documented boundary rule (dashboard.go's ChannelActivity doc comment): an
// entity created exactly at now-24h counts IN the 24h window.
func TestDashboardStore_ChannelActivity_FR23_24hBoundaryIsInclusive(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	now := time.Now().UTC()
	exactlyAtBoundary := now.Add(-24 * time.Hour)
	justPastBoundary := now.Add(-24*time.Hour - time.Second)

	onBoundary, err := s.Research().SaveNote(ctx, store.SaveNoteInput{ChannelID: ch.ID, ThreadTitle: "On Boundary", Text: "on boundary", AuthorPersonID: creator.ID})
	require.NoError(t, err)
	setResearchNoteCreatedAt(t, ctx, db, onBoundary.ID, exactlyAtBoundary)

	pastBoundary, err := s.Research().SaveNote(ctx, store.SaveNoteInput{ChannelID: ch.ID, ThreadTitle: "Past Boundary", Text: "past boundary", AuthorPersonID: creator.ID})
	require.NoError(t, err)
	setResearchNoteCreatedAt(t, ctx, db, pastBoundary.ID, justPastBoundary)

	activity, err := s.Dashboard().ChannelActivity(ctx, ch.ID, now)
	require.NoError(t, err)

	assert.Equal(t, 1, activity.Last24h.ResearchNotes, "an entity created exactly at the 24h boundary must count IN the 24h window (inclusive lower bound)")
	assert.Equal(t, 2, activity.Last7d.ResearchNotes, "both notes fall within the wider 7d window regardless of the 24h boundary")
}

// TestDashboardStore_ChannelActivity_FR23_LinkedVideos_CreatedAtNotResolvedAt
// is FR23's load-bearing "newly-linked video" definition: a match FIRST
// recorded outside the window but resolved inside it must never count, a
// match recorded inside the window (regardless of resolution state) must
// count, and a published video with no match row at all must never count.
func TestDashboardStore_ChannelActivity_FR23_LinkedVideos_CreatedAtNotResolvedAt(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	now := time.Now().UTC()

	// A match FIRST recorded 8 days ago (outside both windows), then
	// resolved (confirmed) just now -- must not count as newly-linked in
	// EITHER window: the query reads vsm.created_at only, never
	// resolved_at, so this proves the query can't accidentally pick up
	// resolved_at even by coincidence.
	oldMatchID := matchAt(t, ctx, s, db, ch, "old-resolved-recently", now.Add(-8*24*time.Hour))
	require.NoError(t, s.Matches().Resolve(ctx, oldMatchID, creator.ID, true, nil))

	// A match created 1h ago, left pending -- counts purely by created_at;
	// resolution state is irrelevant to this count.
	matchAt(t, ctx, s, db, ch, "new-pending", now.Add(-1*time.Hour))

	// A video published within the window with no match row at all -- must
	// never count as newly-linked.
	require.NoError(t, s.Sync().UpsertVideos(ctx, ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: "yt-no-match-" + uuid.NewString(), Title: "No Match",
		PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: ptrTime(now.Add(-1 * time.Hour)), LastSyncedAt: time.Now(),
	}}))

	activity, err := s.Dashboard().ChannelActivity(ctx, ch.ID, now)
	require.NoError(t, err)

	assert.Equal(t, 1, activity.Last24h.LinkedVideos, "only the match created 1h ago counts -- the 8-day-old match resolved just now must not")
	assert.Equal(t, 1, activity.Last7d.LinkedVideos, "the 8-day-old match falls outside the 7d window regardless of when it was resolved")
}

// ── FR24: outcome count + prior-period trend ────────────────────────────────

func TestDashboardStore_ChannelActivity_FR24_OutcomesRequireRecordedMetrics(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, _ := setupChannel(t, ctx, s)

	now := time.Now().UTC()

	withMetrics := publishedVideoAt(t, ctx, s, db, ch, "with-metrics", now.Add(-1*time.Hour))
	require.NoError(t, s.Sync().UpsertMetrics(ctx, []store.VideoMetrics{{SyncedVideoID: withMetrics, Views: ptrInt64(100), MeasuredAt: time.Now()}}))

	// Published, but no video_metrics row -- must not count (FR24: an
	// outcome requires recorded metrics, not merely being published).
	publishedVideoAt(t, ctx, s, db, ch, "no-metrics", now.Add(-1*time.Hour))

	activity, err := s.Dashboard().ChannelActivity(ctx, ch.ID, now)
	require.NoError(t, err)

	assert.Equal(t, 1, activity.Outcomes24h.Count, "only the published video WITH a recorded metrics row counts as an outcome")
	assert.Equal(t, 1, activity.Outcomes7d.Count)
}

func TestDashboardStore_ChannelActivity_FR24_TrendBetterWhenCurrentExceedsPrior(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, _ := setupChannel(t, ctx, s)

	now := time.Now().UTC()

	for i, age := range []time.Duration{1 * time.Hour, 2 * time.Hour} {
		id := publishedVideoAt(t, ctx, s, db, ch, fmt.Sprintf("cur24-%d", i), now.Add(-age))
		require.NoError(t, s.Sync().UpsertMetrics(ctx, []store.VideoMetrics{{SyncedVideoID: id, Views: ptrInt64(1), MeasuredAt: time.Now()}}))
	}
	// Prior 24h period is [now-48h, now-24h) -- 30h ago lands inside it.
	priorID := publishedVideoAt(t, ctx, s, db, ch, "prior24", now.Add(-30*time.Hour))
	require.NoError(t, s.Sync().UpsertMetrics(ctx, []store.VideoMetrics{{SyncedVideoID: priorID, Views: ptrInt64(1), MeasuredAt: time.Now()}}))

	activity, err := s.Dashboard().ChannelActivity(ctx, ch.ID, now)
	require.NoError(t, err)

	assert.Equal(t, 2, activity.Outcomes24h.Count)
	assert.Equal(t, 1, activity.Outcomes24h.PriorCount)
	assert.Equal(t, store.TrendBetter, activity.Outcomes24h.Trend, "2 current outcomes vs. 1 prior must classify as Better")
}

func TestDashboardStore_ChannelActivity_FR24_TrendWorseWhenCurrentBelowPrior(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, _ := setupChannel(t, ctx, s)

	now := time.Now().UTC()

	curID := publishedVideoAt(t, ctx, s, db, ch, "cur24", now.Add(-1*time.Hour))
	require.NoError(t, s.Sync().UpsertMetrics(ctx, []store.VideoMetrics{{SyncedVideoID: curID, Views: ptrInt64(1), MeasuredAt: time.Now()}}))

	for i, age := range []time.Duration{26 * time.Hour, 30 * time.Hour} {
		id := publishedVideoAt(t, ctx, s, db, ch, fmt.Sprintf("prior24-%d", i), now.Add(-age))
		require.NoError(t, s.Sync().UpsertMetrics(ctx, []store.VideoMetrics{{SyncedVideoID: id, Views: ptrInt64(1), MeasuredAt: time.Now()}}))
	}

	activity, err := s.Dashboard().ChannelActivity(ctx, ch.ID, now)
	require.NoError(t, err)

	assert.Equal(t, 1, activity.Outcomes24h.Count)
	assert.Equal(t, 2, activity.Outcomes24h.PriorCount)
	assert.Equal(t, store.TrendWorse, activity.Outcomes24h.Trend, "1 current outcome vs. 2 prior must classify as Worse")
}

// TestDashboardStore_ChannelActivity_FR24_ZeroZeroTrendIsSameNotBetter is
// FR24's explicit 0-vs-0 case: a Channel with zero outcomes in both the
// current and prior period must classify as Same, never Better.
func TestDashboardStore_ChannelActivity_FR24_ZeroZeroTrendIsSameNotBetter(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch, _ := setupChannel(t, ctx, s)

	activity, err := s.Dashboard().ChannelActivity(ctx, ch.ID, time.Now().UTC())
	require.NoError(t, err)

	assert.Equal(t, 0, activity.Outcomes24h.Count)
	assert.Equal(t, 0, activity.Outcomes24h.PriorCount)
	assert.Equal(t, store.TrendSame, activity.Outcomes24h.Trend, "0 outcomes vs. 0 prior outcomes must be Same, never Better")
	assert.Equal(t, store.TrendSame, activity.Outcomes7d.Trend)
}

// ── FR26: zero-activity Channel ─────────────────────────────────────────────

func TestDashboardStore_ChannelActivity_FR26_ZeroActivityChannelReturnsZeroedStructNoError(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch, _ := setupChannel(t, ctx, s)

	activity, err := s.Dashboard().ChannelActivity(ctx, ch.ID, time.Now().UTC())
	require.NoError(t, err, "a brand-new Channel with zero activity must return a real zero-valued ChannelActivity, never an error")

	assert.Equal(t, store.ActivityCounts{}, activity.Last24h)
	assert.Equal(t, store.ActivityCounts{}, activity.Last7d)
	assert.Equal(t, store.OutcomeWindow{Count: 0, PriorCount: 0, Trend: store.TrendSame}, activity.Outcomes24h)
	assert.Equal(t, store.OutcomeWindow{Count: 0, PriorCount: 0, Trend: store.TrendSame}, activity.Outcomes7d)
}

// ── NFR3: bounded round trips ────────────────────────────────────────────────

// TestDashboardStore_ChannelActivity_NFR3_ExactlyTwoRoundTripsRegardlessOfVolume
// mirrors TestAccessStore_ChannelsWithRoleForPerson_IsSingleQuery's pattern
// (store_integration_test.go): 20 of everything ChannelActivity counts must
// still resolve in exactly the two SQL round trips dashboard.go's doc
// comment promises (activityCountsQuery, outcomesQuery) -- never a
// per-entity or per-window Go-side loop.
func TestDashboardStore_ChannelActivity_NFR3_ExactlyTwoRoundTripsRegardlessOfVolume(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	now := time.Now().UTC()
	for i := 0; i < 20; i++ {
		note, err := s.Research().SaveNote(ctx, store.SaveNoteInput{ChannelID: ch.ID, ThreadTitle: fmt.Sprintf("T%d", i), Text: "note", AuthorPersonID: creator.ID})
		require.NoError(t, err)
		setResearchNoteCreatedAt(t, ctx, db, note.ID, now.Add(-1*time.Hour))

		matchAt(t, ctx, s, db, ch, fmt.Sprintf("m%d", i), now.Add(-1*time.Hour))

		id := publishedVideoAt(t, ctx, s, db, ch, fmt.Sprintf("pv%d", i), now.Add(-1*time.Hour))
		require.NoError(t, s.Sync().UpsertMetrics(ctx, []store.VideoMetrics{{SyncedVideoID: id, Views: ptrInt64(1), MeasuredAt: time.Now()}}))
	}

	counter := &queryCounter{}
	traced := tracedStore(t, ctx, db, counter)

	before := counter.n.Load()
	activity, err := traced.Dashboard().ChannelActivity(ctx, ch.ID, now)
	require.NoError(t, err)
	require.Equal(t, 20, activity.Last24h.ResearchNotes, "sanity: the fixture actually seeded 20 notes into the 24h window")

	assert.Equal(t, int64(2), counter.n.Load()-before,
		"ChannelActivity must issue exactly two SQL round trips regardless of how much activity a Channel has accumulated (NFR3)")
}

// ── Multi-tenant isolation ───────────────────────────────────────────────────

func TestDashboardStore_ChannelActivity_MultiTenantIsolation(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	chA, creatorA := setupChannel(t, ctx, s)
	chB, creatorB := setupChannel(t, ctx, s)

	now := time.Now().UTC()

	noteA, err := s.Research().SaveNote(ctx, store.SaveNoteInput{ChannelID: chA.ID, ThreadTitle: "A", Text: "a", AuthorPersonID: creatorA.ID})
	require.NoError(t, err)
	setResearchNoteCreatedAt(t, ctx, db, noteA.ID, now.Add(-1*time.Hour))

	noteB, err := s.Research().SaveNote(ctx, store.SaveNoteInput{ChannelID: chB.ID, ThreadTitle: "B", Text: "b", AuthorPersonID: creatorB.ID})
	require.NoError(t, err)
	setResearchNoteCreatedAt(t, ctx, db, noteB.ID, now.Add(-1*time.Hour))

	activityA, err := s.Dashboard().ChannelActivity(ctx, chA.ID, now)
	require.NoError(t, err)
	assert.Equal(t, 1, activityA.Last24h.ResearchNotes, "chA's activity must not include chB's note")

	activityB, err := s.Dashboard().ChannelActivity(ctx, chB.ID, now)
	require.NoError(t, err)
	assert.Equal(t, 1, activityB.Last24h.ResearchNotes, "chB's activity must not include chA's note")
}
