//go:build integration

// video_script_integration_test.go covers VideoScriptStore (migration
// 010, issues #1823/#1824): Propose's viable-verdict + on-channel gates
// (FR36, LB3), Greenlight/Deny/Archive's exhaustive transition set
// (FR40), Archive's publish freeze and its atomicity (FR39), IsPublished's
// freeze matrix, ListDetailByChannel's join + Channel scoping, and
// migration 010's schedule_entry backfill (FR45). Split into its own file
// rather than growing store_integration_test.go further (that file was
// already large before this milestone; issue #1824's Testing section
// explicitly allows either) -- same package/build tag/harness, so
// newStore/setupChannel/ptrTime (defined in store_integration_test.go)
// are reused directly.
package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/migrate/schema"
	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// setupVideoScriptChannel is the shared fixture every Propose/Greenlight/
// Deny/Archive/ListDetailByChannel test below starts from: a Channel/
// creator, a fresh Idea with a viable verdict, and one active Strategy
// grounded on that same verdict (FR36 -- a video_script cannot exist
// without a grounding Strategy).
func setupVideoScriptChannel(t *testing.T, ctx context.Context, s *store.Store, ideaTitle string) (ch store.Channel, creator store.Person, idea store.Idea, verdict store.Verdict, strategy store.StrategyDetail) {
	t.Helper()

	ch, creator = setupChannel(t, ctx, s)

	idea, err := s.Ideas().Create(ctx, ch.ID, ideaTitle, creator.ID)
	require.NoError(t, err)

	verdict, err = s.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: ideaTitle + " looks strong", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	strategy, err = s.Strategies().Save(ctx, store.SaveStrategyInput{
		ChannelID: ch.ID, Title: ideaTitle + " Strategy", Active: true,
		VerdictIDs: []uuid.UUID{verdict.ID}, CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	return ch, creator, idea, verdict, strategy
}

// proposeVideoScript is the shared Propose call every Greenlight/Deny/
// Archive/ListDetailByChannel test below starts from: a freshly-proposed
// video_script on setupVideoScriptChannel's fixture.
func proposeVideoScript(t *testing.T, ctx context.Context, s *store.Store, ch store.Channel, creator store.Person, verdict store.Verdict, strategy store.StrategyDetail, title string) store.VideoScript {
	t.Helper()

	script, err := s.VideoScripts().Propose(ctx, store.ProposeVideoScriptInput{
		ChannelID: ch.ID, VerdictID: verdict.ID, StrategyID: strategy.ID,
		Title: title, ScriptText: "script text for " + title, CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)
	return script
}

// syncOneVideo upserts one synced_video for ch and returns it by looking
// it back up via its unique youtube_video_id -- publishedAt nil means not
// yet published.
func syncOneVideo(t *testing.T, ctx context.Context, s *store.Store, ch store.Channel, title string, publishedAt *time.Time) store.SyncedVideo {
	t.Helper()

	ytID := "yt-" + uuid.NewString()
	require.NoError(t, s.Sync().UpsertVideos(ctx, ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: ytID, Title: title,
		PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: publishedAt, LastSyncedAt: time.Now(),
	}}))

	synced, _, err := s.Sync().ListSchedule(ctx, ch.ID, nil, nil, true, 0)
	require.NoError(t, err)
	for _, sv := range synced {
		if sv.YouTubeVideoID == ytID {
			return sv
		}
	}
	t.Fatalf("synced video %s not found after upsert", ytID)
	return store.SyncedVideo{}
}

// recordVideoScriptMatch inserts a video_schedule_match row wired to
// scriptID via video_script_id (migration 010's FR45 re-anchor column) --
// directly by SQL, for a fixture state (an arbitrary confidence, an
// arbitrary state) match_integration_test.go's ListCandidates/Record
// coverage of MatchStore.Record itself (#1829) doesn't need to exercise.
func recordVideoScriptMatch(t *testing.T, ctx context.Context, db *dbtest.Postgres, scriptID, syncedVideoID uuid.UUID, state store.MatchState) {
	t.Helper()

	_, err := db.Pool.Exec(ctx, `
		INSERT INTO video_schedule_match (synced_video_id, video_script_id, confidence, state)
		VALUES ($1, $2, 0.9, $3)
	`, syncedVideoID, scriptID, state)
	require.NoError(t, err)
}

// ── VideoScriptStore.Propose (FR36, LB3) ────────────────────────────────────

func TestVideoScriptStore_Propose_ViableVerdict_Succeeds(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch, creator, idea, verdict, strategy := setupVideoScriptChannel(t, ctx, s, "Propose Viable Idea")

	script, err := s.VideoScripts().Propose(ctx, store.ProposeVideoScriptInput{
		ChannelID: ch.ID, VerdictID: verdict.ID, StrategyID: strategy.ID,
		Title: "Propose Viable Script", ScriptText: "the script text", CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err, "Propose must accept a viable verdict (FR36)")

	assert.Equal(t, store.VideoScriptStatusProposed, script.Status, "status must always be proposed on insert")
	assert.Equal(t, idea.ID, script.IdeaID, "idea_id must be derived from the verdict, never disagree with it (LB3)")
	assert.Equal(t, verdict.ID, script.VerdictID)
	assert.Equal(t, strategy.ID, script.StrategyID)
}

func TestVideoScriptStore_Propose_RejectsNonViableVerdicts_NothingInserted(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	// A Strategy grounded on a separate, genuinely-viable verdict so the
	// rejections below are attributable to the verdict check, not a
	// missing/invalid strategy.
	groundingIdea, err := s.Ideas().Create(ctx, ch.ID, "Grounding Idea", creator.ID)
	require.NoError(t, err)
	groundingVerdict, err := s.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: groundingIdea.ID, Verdict: store.VerdictViable, Reasoning: "grounding", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	strategy, err := s.Strategies().Save(ctx, store.SaveStrategyInput{
		ChannelID: ch.ID, Title: "Grounding Strategy", Active: true,
		VerdictIDs: []uuid.UUID{groundingVerdict.ID}, CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	idea, err := s.Ideas().Create(ctx, ch.ID, "Idea Needing Judgement", creator.ID)
	require.NoError(t, err)

	notViable, err := s.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictNotViable, Reasoning: "saturated niche", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	_, err = s.VideoScripts().Propose(ctx, store.ProposeVideoScriptInput{
		ChannelID: ch.ID, VerdictID: notViable.ID, StrategyID: strategy.ID,
		Title: "Not Viable Script", ScriptText: "text", CreatedByPersonID: creator.ID,
	})
	assert.ErrorIs(t, err, store.ErrVerdictNotViable, "Propose must reject a not-viable verdict (FR36)")

	needsMore, err := s.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictNeedsMoreResearch, Reasoning: "more comps needed", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	_, err = s.VideoScripts().Propose(ctx, store.ProposeVideoScriptInput{
		ChannelID: ch.ID, VerdictID: needsMore.ID, StrategyID: strategy.ID,
		Title: "Needs More Research Script", ScriptText: "text", CreatedByPersonID: creator.ID,
	})
	assert.ErrorIs(t, err, store.ErrVerdictNotViable, "Propose must reject a needs-more-research verdict (FR36)")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM video_script WHERE idea_id = $1`, idea.ID).Scan(&count))
	assert.Equal(t, 0, count, "the two rejected Propose calls must not have written any video_script row")
}

func TestVideoScriptStore_Propose_SupersededVerdictVersionStillAccepted(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	idea, err := s.Ideas().Create(ctx, ch.ID, "Superseded Verdict Idea", creator.ID)
	require.NoError(t, err)

	v1, err := s.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "first pass", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	v2, err := s.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "re-confirmed", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	require.NotEqual(t, v1.ID, v2.ID)

	strategy, err := s.Strategies().Save(ctx, store.SaveStrategyInput{
		ChannelID: ch.ID, Title: "Strategy", Active: true,
		VerdictIDs: []uuid.UUID{v2.ID}, CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	script, err := s.VideoScripts().Propose(ctx, store.ProposeVideoScriptInput{
		ChannelID: ch.ID, VerdictID: v1.ID, StrategyID: strategy.ID,
		Title: "Video From Superseded Verdict", ScriptText: "text", CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err, "Propose must accept a superseded-but-still-viable verdict version, not just the current one (LB3)")
	assert.Equal(t, v1.ID, script.VerdictID, "the pinned verdict must be exactly the version passed in, not the idea's current verdict")
	assert.Equal(t, idea.ID, script.IdeaID)
}

func TestVideoScriptStore_Propose_RejectsStrategyFromDifferentChannel(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch1, creator1, idea1, verdict1, _ := setupVideoScriptChannel(t, ctx, s, "Channel One Idea")
	_, _, _, _, strategy2 := setupVideoScriptChannel(t, ctx, s, "Channel Two Idea")

	_, err := s.VideoScripts().Propose(ctx, store.ProposeVideoScriptInput{
		ChannelID: ch1.ID, VerdictID: verdict1.ID, StrategyID: strategy2.ID,
		Title: "Cross-Channel Strategy Script", ScriptText: "text", CreatedByPersonID: creator1.ID,
	})
	assert.ErrorIs(t, err, store.ErrStrategyNotFound, "Propose must reject a strategy_id that does not belong to channel_id")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM video_script WHERE idea_id = $1`, idea1.ID).Scan(&count))
	assert.Equal(t, 0, count, "the rejected Propose call must not have written any video_script row")
}

// ── VideoScriptStore.Greenlight / Deny (FR37/FR38/FR40) ─────────────────────

func TestVideoScriptStore_Greenlight_ProposedToGreenlit_OtherStatesRejected(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch, creator, _, verdict, strategy := setupVideoScriptChannel(t, ctx, s, "Greenlight Idea")

	script := proposeVideoScript(t, ctx, s, ch, creator, verdict, strategy, "Greenlight Script")
	require.NoError(t, s.VideoScripts().Greenlight(ctx, script.ID, creator.ID))

	got, err := s.VideoScripts().GetByID(ctx, script.ID)
	require.NoError(t, err)
	assert.Equal(t, store.VideoScriptStatusGreenlit, got.Status)
	require.NotNil(t, got.DecidedByPersonID)
	assert.Equal(t, creator.ID, *got.DecidedByPersonID)
	require.NotNil(t, got.DecidedAt)

	err = s.VideoScripts().Greenlight(ctx, script.ID, creator.ID)
	assert.ErrorIs(t, err, store.ErrVideoScriptTransition, "Greenlight on an already-greenlit script must be rejected (FR40)")

	deniedScript := proposeVideoScript(t, ctx, s, ch, creator, verdict, strategy, "Denied Script")
	require.NoError(t, s.VideoScripts().Deny(ctx, deniedScript.ID, creator.ID))
	err = s.VideoScripts().Greenlight(ctx, deniedScript.ID, creator.ID)
	assert.ErrorIs(t, err, store.ErrVideoScriptTransition, "Greenlight on a denied script must be rejected -- denied is terminal (FR40)")

	archivedScript := proposeVideoScript(t, ctx, s, ch, creator, verdict, strategy, "Archived Script")
	require.NoError(t, s.VideoScripts().Greenlight(ctx, archivedScript.ID, creator.ID))
	require.NoError(t, s.VideoScripts().Archive(ctx, archivedScript.ID, creator.ID))
	err = s.VideoScripts().Greenlight(ctx, archivedScript.ID, creator.ID)
	assert.ErrorIs(t, err, store.ErrVideoScriptTransition, "Greenlight on an archived script must be rejected (FR40)")
}

func TestVideoScriptStore_Deny_ProposedToDenied_OtherStatesRejected(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch, creator, _, verdict, strategy := setupVideoScriptChannel(t, ctx, s, "Deny Idea")

	script := proposeVideoScript(t, ctx, s, ch, creator, verdict, strategy, "Deny Script")
	require.NoError(t, s.VideoScripts().Deny(ctx, script.ID, creator.ID))

	got, err := s.VideoScripts().GetByID(ctx, script.ID)
	require.NoError(t, err)
	assert.Equal(t, store.VideoScriptStatusDenied, got.Status)
	require.NotNil(t, got.DecidedByPersonID)
	assert.Equal(t, creator.ID, *got.DecidedByPersonID)
	require.NotNil(t, got.DecidedAt)

	err = s.VideoScripts().Deny(ctx, script.ID, creator.ID)
	assert.ErrorIs(t, err, store.ErrVideoScriptTransition, "Deny on an already-denied script must be rejected -- denied is terminal (FR40)")

	greenlitScript := proposeVideoScript(t, ctx, s, ch, creator, verdict, strategy, "Greenlit Then Deny Script")
	require.NoError(t, s.VideoScripts().Greenlight(ctx, greenlitScript.ID, creator.ID))
	err = s.VideoScripts().Deny(ctx, greenlitScript.ID, creator.ID)
	assert.ErrorIs(t, err, store.ErrVideoScriptTransition, "Deny on a greenlit script must be rejected (FR40)")
}

// ── VideoScriptStore.Archive / publish freeze (FR39/FR40) ───────────────────

func TestVideoScriptStore_Archive_GreenlitNoMatch_Succeeds(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch, creator, _, verdict, strategy := setupVideoScriptChannel(t, ctx, s, "Archive No Match Idea")
	script := proposeVideoScript(t, ctx, s, ch, creator, verdict, strategy, "Archive No Match Script")
	require.NoError(t, s.VideoScripts().Greenlight(ctx, script.ID, creator.ID))

	require.NoError(t, s.VideoScripts().Archive(ctx, script.ID, creator.ID))

	got, err := s.VideoScripts().GetByID(ctx, script.ID)
	require.NoError(t, err)
	assert.Equal(t, store.VideoScriptStatusArchived, got.Status)
	require.NotNil(t, got.DecidedByPersonID)
	assert.Equal(t, creator.ID, *got.DecidedByPersonID)
}

func TestVideoScriptStore_Archive_PendingMatchToPublishedVideo_Succeeds(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator, _, verdict, strategy := setupVideoScriptChannel(t, ctx, s, "Archive Pending Match Idea")
	script := proposeVideoScript(t, ctx, s, ch, creator, verdict, strategy, "Archive Pending Match Script")
	require.NoError(t, s.VideoScripts().Greenlight(ctx, script.ID, creator.ID))

	publishedAt := time.Now().Add(-time.Hour)
	video := syncOneVideo(t, ctx, s, ch, "Pending Match Video", &publishedAt)
	recordVideoScriptMatch(t, ctx, db, script.ID, video.ID, store.MatchStatePending)

	err := s.VideoScripts().Archive(ctx, script.ID, creator.ID)
	require.NoError(t, err, "a pending match to a published video must NOT freeze Archive -- only auto/confirmed matches do (FR39)")

	got, err := s.VideoScripts().GetByID(ctx, script.ID)
	require.NoError(t, err)
	assert.Equal(t, store.VideoScriptStatusArchived, got.Status)
}

func TestVideoScriptStore_Archive_AutoMatchToPublishedVideo_Frozen(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator, _, verdict, strategy := setupVideoScriptChannel(t, ctx, s, "Archive Auto Match Idea")
	script := proposeVideoScript(t, ctx, s, ch, creator, verdict, strategy, "Archive Auto Match Script")
	require.NoError(t, s.VideoScripts().Greenlight(ctx, script.ID, creator.ID))

	publishedAt := time.Now().Add(-time.Hour)
	video := syncOneVideo(t, ctx, s, ch, "Auto Match Video", &publishedAt)
	recordVideoScriptMatch(t, ctx, db, script.ID, video.ID, store.MatchStateAuto)

	err := s.VideoScripts().Archive(ctx, script.ID, creator.ID)
	assert.ErrorIs(t, err, store.ErrVideoScriptPublished, "an auto match to a published video must freeze Archive (FR39)")

	got, err := s.VideoScripts().GetByID(ctx, script.ID)
	require.NoError(t, err)
	assert.Equal(t, store.VideoScriptStatusGreenlit, got.Status, "a frozen Archive attempt must leave status unchanged")
}

func TestVideoScriptStore_Archive_ConfirmedMatchToUnpublishedVideo_Succeeds(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator, _, verdict, strategy := setupVideoScriptChannel(t, ctx, s, "Archive Confirmed Unpublished Idea")
	script := proposeVideoScript(t, ctx, s, ch, creator, verdict, strategy, "Archive Confirmed Unpublished Script")
	require.NoError(t, s.VideoScripts().Greenlight(ctx, script.ID, creator.ID))

	video := syncOneVideo(t, ctx, s, ch, "Confirmed Unpublished Video", nil)
	recordVideoScriptMatch(t, ctx, db, script.ID, video.ID, store.MatchStateConfirmed)

	err := s.VideoScripts().Archive(ctx, script.ID, creator.ID)
	require.NoError(t, err, "a confirmed match to a video that has not published yet (scheduled but not live) must NOT freeze Archive (FR39)")

	got, err := s.VideoScripts().GetByID(ctx, script.ID)
	require.NoError(t, err)
	assert.Equal(t, store.VideoScriptStatusArchived, got.Status)
}

func TestVideoScriptStore_Archive_ProposedRejected(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch, creator, _, verdict, strategy := setupVideoScriptChannel(t, ctx, s, "Archive Proposed Idea")
	script := proposeVideoScript(t, ctx, s, ch, creator, verdict, strategy, "Archive Proposed Script")

	err := s.VideoScripts().Archive(ctx, script.ID, creator.ID)
	assert.ErrorIs(t, err, store.ErrVideoScriptTransition, "Archive on a merely-proposed script must be rejected -- only greenlit->archived is allowed (FR40)")

	got, err := s.VideoScripts().GetByID(ctx, script.ID)
	require.NoError(t, err)
	assert.Equal(t, store.VideoScriptStatusProposed, got.Status)
}

// ── VideoScriptStore.IsPublished freeze matrix (FR39) ───────────────────────

func TestVideoScriptStore_IsPublished_MatchesArchiveFreezeMatrix(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator, _, verdict, strategy := setupVideoScriptChannel(t, ctx, s, "IsPublished Idea")

	noMatch := proposeVideoScript(t, ctx, s, ch, creator, verdict, strategy, "IsPublished No Match Script")
	published, err := s.VideoScripts().IsPublished(ctx, noMatch.ID)
	require.NoError(t, err)
	assert.False(t, published, "a script with no match at all must not report published")

	pendingPublished := proposeVideoScript(t, ctx, s, ch, creator, verdict, strategy, "IsPublished Pending Script")
	publishedAt := time.Now().Add(-time.Hour)
	video1 := syncOneVideo(t, ctx, s, ch, "IsPublished Pending Video", &publishedAt)
	recordVideoScriptMatch(t, ctx, db, pendingPublished.ID, video1.ID, store.MatchStatePending)
	published, err = s.VideoScripts().IsPublished(ctx, pendingPublished.ID)
	require.NoError(t, err)
	assert.False(t, published, "a pending match must not count as published, even to a video that has already published")

	autoPublished := proposeVideoScript(t, ctx, s, ch, creator, verdict, strategy, "IsPublished Auto Script")
	video2 := syncOneVideo(t, ctx, s, ch, "IsPublished Auto Video", &publishedAt)
	recordVideoScriptMatch(t, ctx, db, autoPublished.ID, video2.ID, store.MatchStateAuto)
	published, err = s.VideoScripts().IsPublished(ctx, autoPublished.ID)
	require.NoError(t, err)
	assert.True(t, published, "an auto match to a published video must count as published")

	confirmedUnpublished := proposeVideoScript(t, ctx, s, ch, creator, verdict, strategy, "IsPublished Confirmed Unpublished Script")
	video3 := syncOneVideo(t, ctx, s, ch, "IsPublished Confirmed Unpublished Video", nil)
	recordVideoScriptMatch(t, ctx, db, confirmedUnpublished.ID, video3.ID, store.MatchStateConfirmed)
	published, err = s.VideoScripts().IsPublished(ctx, confirmedUnpublished.ID)
	require.NoError(t, err)
	assert.False(t, published, "a confirmed match to a video that has not published yet must not count as published")

	confirmedPublished := proposeVideoScript(t, ctx, s, ch, creator, verdict, strategy, "IsPublished Confirmed Published Script")
	video4 := syncOneVideo(t, ctx, s, ch, "IsPublished Confirmed Published Video", &publishedAt)
	recordVideoScriptMatch(t, ctx, db, confirmedPublished.ID, video4.ID, store.MatchStateConfirmed)
	published, err = s.VideoScripts().IsPublished(ctx, confirmedPublished.ID)
	require.NoError(t, err)
	assert.True(t, published, "a confirmed match to a published video must count as published")
}

// ── VideoScriptStore.ListDetailByChannel ─────────────────────────────────────

func TestVideoScriptStore_ListDetailByChannel_JoinsAndScopesByChannel(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator, idea, verdict, strategy := setupVideoScriptChannel(t, ctx, s, "Detail Idea")
	script := proposeVideoScript(t, ctx, s, ch, creator, verdict, strategy, "Detail Script")
	require.NoError(t, s.VideoScripts().Greenlight(ctx, script.ID, creator.ID))

	publishedAt := time.Now().Add(-time.Hour)
	video := syncOneVideo(t, ctx, s, ch, "Detail Video", &publishedAt)
	recordVideoScriptMatch(t, ctx, db, script.ID, video.ID, store.MatchStateConfirmed)

	otherCh, otherCreator, _, otherVerdict, otherStrategy := setupVideoScriptChannel(t, ctx, s, "Other Channel Idea")
	otherScript := proposeVideoScript(t, ctx, s, otherCh, otherCreator, otherVerdict, otherStrategy, "Other Channel Script")

	details, err := s.VideoScripts().ListDetailByChannel(ctx, ch.ID)
	require.NoError(t, err)
	require.Len(t, details, 1, "must not leak another Channel's video_script rows")

	d := details[0]
	assert.Equal(t, script.ID, d.Script.ID)
	assert.Equal(t, verdict.Version, d.VerdictVersion)
	assert.Equal(t, store.VerdictViable, d.Verdict)
	assert.Equal(t, idea.Title, d.IdeaTitle)
	assert.Equal(t, strategy.Title, d.StrategyTitle)
	assert.True(t, d.Published, "a confirmed match to a published video must set Published")

	otherDetails, err := s.VideoScripts().ListDetailByChannel(ctx, otherCh.ID)
	require.NoError(t, err)
	require.Len(t, otherDetails, 1)
	assert.Equal(t, otherScript.ID, otherDetails[0].Script.ID)
	assert.False(t, otherDetails[0].Published, "a script with no match must not be reported Published")
	for _, od := range otherDetails {
		assert.NotEqual(t, script.ID, od.Script.ID, "this Channel's script must never leak into another Channel's list")
	}
}

// ── Migration 010 schedule_entry backfill (FR45) ─────────────────────────────

// TestMigration010_ScheduleEntryBackfill_DerivesStrategyDropsUngroundable
// proves migration 010's best-effort schedule_entry -> video_script
// backfill: a schedule_entry whose verdict grounds an active Strategy on
// the same Channel gets a video_script row with strategy_id derived via
// strategy_verdict, and its confirmed video_schedule_match is re-pointed
// at that row via video_script_id; a schedule_entry whose verdict grounds
// no Strategy is DROPPED (no video_script row at all, no placeholder
// Strategy synthesized), and its own already-confirmed match's
// video_script_id is left NULL -- the accepted, explicitly-decided data
// loss (FR45), not a bug.
func TestMigration010_ScheduleEntryBackfill_DerivesStrategyDropsUngroundable(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Migrate(9), "apply every migration up to (not including) 010, before video_script exists")

	s := store.New(db.Pool)
	ch, creator := setupChannel(t, ctx, s)

	// -- Groundable: verdict1 grounds strategy1 via strategy_verdict. -------
	idea1, err := s.Ideas().Create(ctx, ch.ID, "Groundable Idea", creator.ID)
	require.NoError(t, err)
	verdict1, err := s.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea1.ID, Verdict: store.VerdictViable, Reasoning: "groundable", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	// The schema is only migrated to 9 here -- pre-migration-011, `strategy.
	// cadence` is still NOT NULL with no default (migration 008's original
	// shape), but StrategyStore.Save (as of #1833/FR47) never populates it.
	// Seed the row by SQL directly instead, exactly as a pre-011 caller
	// would have, mirroring the video_schedule_match seed below for the
	// same reason (a current store method that can't write a historical
	// schema shape).
	var strategy1ID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO strategy (channel_id, title, cadence, active, created_by_person_id)
		VALUES ($1, $2, 'weekly', true, $3)
		RETURNING id
	`, ch.ID, "Groundable Strategy", creator.ID).Scan(&strategy1ID))
	_, err = db.Pool.Exec(ctx, `INSERT INTO strategy_verdict (strategy_id, verdict_id) VALUES ($1, $2)`, strategy1ID, verdict1.ID)
	require.NoError(t, err)
	// store.ScheduleStore (store/schedule.go) no longer exists (deleted
	// outright by #1835's retirement task) -- seed the pre-010
	// schedule_entry row directly by SQL instead, exactly as a pre-010
	// caller would have (mirroring the video_schedule_match seed below for
	// the same reason: a current store method that can't write a
	// historical schema shape).
	var entry1ID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO schedule_entry (channel_id, idea_id, verdict_id, proposed_publish_at, state, approved_by_person_id, approved_at, created_by_person_id)
		VALUES ($1, $2, $3, $4, 'committed', $5, NOW(), $5)
		RETURNING id
	`, ch.ID, idea1.ID, verdict1.ID, time.Now().Add(24*time.Hour), creator.ID).Scan(&entry1ID))

	video1 := syncOneVideo(t, ctx, s, ch, "Groundable Video", ptrTime(time.Now().Add(-time.Hour)))
	// The schema is only migrated to 9 here (video_script_id doesn't exist
	// yet, that's what migration 010 itself adds) -- MatchStore.Record, as
	// of #1829, always writes video_script_id, so it cannot run against
	// this pre-010 schema. Seed the row by SQL directly instead, exactly as
	// a pre-010 matcher would have.
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO video_schedule_match (synced_video_id, schedule_entry_id, confidence, state)
		VALUES ($1, $2, $3, $4)
	`, video1.ID, entry1ID, 0.9, store.MatchStateConfirmed)
	require.NoError(t, err)

	// -- Ungroundable: verdict2 has no strategy_verdict row at all. --------
	idea2, err := s.Ideas().Create(ctx, ch.ID, "Ungroundable Idea", creator.ID)
	require.NoError(t, err)
	verdict2, err := s.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea2.ID, Verdict: store.VerdictViable, Reasoning: "ungroundable", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	var entry2ID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO schedule_entry (channel_id, idea_id, verdict_id, proposed_publish_at, state, approved_by_person_id, approved_at, created_by_person_id)
		VALUES ($1, $2, $3, $4, 'committed', $5, NOW(), $5)
		RETURNING id
	`, ch.ID, idea2.ID, verdict2.ID, time.Now().Add(48*time.Hour), creator.ID).Scan(&entry2ID))

	video2 := syncOneVideo(t, ctx, s, ch, "Ungroundable Video", ptrTime(time.Now().Add(-time.Hour)))
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO video_schedule_match (synced_video_id, schedule_entry_id, confidence, state)
		VALUES ($1, $2, $3, $4)
	`, video2.ID, entry2ID, 0.9, store.MatchStateConfirmed)
	require.NoError(t, err)

	require.NoError(t, runner.Migrate(10), "apply 010, running the backfill")

	// The groundable schedule_entry must have produced exactly one
	// video_script row, with strategy_id derived from strategy1 and status
	// mapped from committed -> greenlit.
	var groundedScriptID uuid.UUID
	var groundedStrategyID uuid.UUID
	var groundedStatus string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT id, strategy_id, status FROM video_script WHERE idea_id = $1
	`, idea1.ID).Scan(&groundedScriptID, &groundedStrategyID, &groundedStatus))
	assert.Equal(t, strategy1ID, groundedStrategyID, "strategy_id must be derived via strategy_verdict joined to the schedule_entry's verdict_id")
	assert.Equal(t, "greenlit", groundedStatus, "a committed schedule_entry must backfill to greenlit")

	var groundedMatchScriptID *uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT video_script_id FROM video_schedule_match WHERE synced_video_id = $1
	`, video1.ID).Scan(&groundedMatchScriptID))
	require.NotNil(t, groundedMatchScriptID, "the groundable entry's confirmed match must be re-pointed at the backfilled video_script")
	assert.Equal(t, groundedScriptID, *groundedMatchScriptID)

	// The ungroundable schedule_entry must have produced NO video_script
	// row, and its already-confirmed match must keep video_script_id NULL.
	var ungroundedCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM video_script WHERE idea_id = $1`, idea2.ID).Scan(&ungroundedCount))
	assert.Equal(t, 0, ungroundedCount, "a schedule_entry with no derivable strategy_id must be dropped, not backfilled with a placeholder Strategy (FR45)")

	var ungroundedMatchScriptID *uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT video_script_id FROM video_schedule_match WHERE synced_video_id = $1
	`, video2.ID).Scan(&ungroundedMatchScriptID))
	assert.Nil(t, ungroundedMatchScriptID, "a match on a dropped schedule_entry must keep video_script_id NULL, including an already-confirmed one (accepted data loss, FR45)")
}
