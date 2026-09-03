//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See //libs/go/dbtest's README and //libs/go/migrate's
// migrate_integration_test.go for the pattern this file follows: spin up a
// throwaway Postgres via dbtest, apply the package's own real embedded
// migrations (not a hand-copied schema), then exercise the store package's
// public API against it.
//
// These tests exercise exactly what authz_test.go's pure-Go tests cannot:
// real unique/partial-unique index enforcement (one live invite code per
// Channel, at most one live channel_person row per (channel, person) pair),
// the SCD2 close-and-open write path actually hitting Postgres, and the
// authorization functions (authz.go) reading live join-table state through
// the real SQL-backed RoleStore rather than a fake.
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

// newStore provisions an isolated Postgres database via dbtest, applies
// every migration in the package's own embedded schema (schema.Migrations
// -- currently 001 identity plus 002 research/verdict/schedule/outcome,
// #1569), and returns a ready *store.Store plus the underlying
// dbtest.Postgres for tests that need to reach past the store's own API
// (e.g. to assert on row counts directly, close a channel_person row out
// from under the store to prove authz reads live state, or query a `v_`
// read model that has no dedicated store method yet).
func newStore(t *testing.T) (*store.Store, *dbtest.Postgres) {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from the real embedded schema")

	return store.New(db.Pool), db
}

// setupChannel is the migration-002 tests' shared fixture: one Channel
// with a live creator, mirroring setupChannelWithRoles above but without
// the analyst/unassociated/former-creator roles those authz tests need.
func setupChannel(t *testing.T, ctx context.Context, s *store.Store) (store.Channel, store.Person) {
	t.Helper()

	creator, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "creator@example.com", "Creator")
	require.NoError(t, err)

	ch, err := s.Channels().Create(ctx, "yt-"+uuid.NewString(), "Channel", creator.ID)
	require.NoError(t, err)

	return ch, creator
}

// ── PersonStore (FR1/FR2) ──────────────────────────────────────────────────

func TestPersonStore_UpsertByGoogleSubject_NewSubCreatesPerson(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)

	p, created, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-new", "a@example.com", "Alice")
	require.NoError(t, err)

	assert.True(t, created, "a never-before-seen google_subject must create a new Person (FR1)")
	assert.NotEqual(t, uuid.Nil, p.ID)
	assert.Equal(t, "sub-new", p.GoogleSubject)
	assert.Equal(t, "a@example.com", p.Email)
	assert.Equal(t, "Alice", p.DisplayName)
}

func TestPersonStore_UpsertByGoogleSubject_ExistingSubReturnsSameNoDuplicate(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)

	first, created, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-existing", "a@example.com", "Alice")
	require.NoError(t, err)
	require.True(t, created)

	second, created2, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-existing", "b@example.com", "Alice B.")
	require.NoError(t, err)

	assert.False(t, created2, "an existing google_subject must not report a newly-created row (FR2)")
	assert.Equal(t, first.ID, second.ID, "an existing google_subject must resolve to the same Person id")
	assert.Equal(t, "b@example.com", second.Email, "email must update on the existing row")
	assert.Equal(t, "Alice B.", second.DisplayName, "display_name must update on the existing row")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM person WHERE google_subject = $1`, "sub-existing",
	).Scan(&count))
	assert.Equal(t, 1, count, "must not create a duplicate person row for an existing google_subject")
}

// ── ChannelStore.Create (FR3, LB2) ─────────────────────────────────────────

func TestChannelStore_Create_CreatesExactlyOneLiveCreatorRow(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)

	creator, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-creator", "c@example.com", "Creator")
	require.NoError(t, err)

	ch, err := s.Channels().Create(ctx, "yt-channel-1", "My Channel", creator.ID)
	require.NoError(t, err)
	assert.Equal(t, store.ConnectionStateConnected, ch.ConnectionState)

	roles, err := s.Roles().RolesFor(ctx, ch.ID, creator.ID)
	require.NoError(t, err)
	assert.Equal(t, []store.Role{store.RoleCreator}, roles)

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM channel_person
		WHERE channel_id = $1 AND person_id = $2 AND valid_to IS NULL
	`, ch.ID, creator.ID).Scan(&count))
	assert.Equal(t, 1, count, "Create must produce exactly one live creator row, not zero or more than one")
}

// ── authz.go against the real SQL-backed RoleStore ─────────────────────────

// channelWithRoles sets up one Channel with a live creator, a live analyst,
// a Person with no row at all, and a Person whose creator row has been
// explicitly closed (valid_to stamped) -- the case that proves an authz
// check reads live join-table state rather than a static/cached field.
type channelWithRoles struct {
	channelID                                      uuid.UUID
	creatorID, analystID, unassociatedID, formerID uuid.UUID
}

func setupChannelWithRoles(t *testing.T, ctx context.Context, s *store.Store, db *dbtest.Postgres) channelWithRoles {
	t.Helper()

	creator, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "creator@example.com", "Creator")
	require.NoError(t, err)

	ch, err := s.Channels().Create(ctx, "yt-"+uuid.NewString(), "Channel", creator.ID)
	require.NoError(t, err)

	analyst, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "analyst@example.com", "Analyst")
	require.NoError(t, err)
	require.NoError(t, s.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst))

	unassociated, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "unassoc@example.com", "Unassociated")
	require.NoError(t, err)

	former, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "former@example.com", "Former Creator")
	require.NoError(t, err)
	require.NoError(t, s.Roles().AddRole(ctx, ch.ID, former.ID, store.RoleCreator))
	// Close the row directly (bypassing the store API, which has no
	// "revoke" method yet) to simulate a Person whose creator role has
	// lapsed -- proves the Can* checks read valid_to IS NULL state, not a
	// static owner field (NFR5).
	_, err = db.Pool.Exec(ctx, `
		UPDATE channel_person SET valid_to = NOW()
		WHERE channel_id = $1 AND person_id = $2 AND valid_to IS NULL
	`, ch.ID, former.ID)
	require.NoError(t, err)

	return channelWithRoles{
		channelID:      ch.ID,
		creatorID:      creator.ID,
		analystID:      analyst.ID,
		unassociatedID: unassociated.ID,
		formerID:       former.ID,
	}
}

func TestAuthz_CreatorOnlyChecks_AgainstRealRoleStore(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	setup := setupChannelWithRoles(t, ctx, s, db)
	rs := s.Roles()

	checks := []struct {
		name string
		fn   func(context.Context, store.RoleStore, uuid.UUID, uuid.UUID) (bool, error)
	}{
		{"CanApprove", store.CanApprove},
		{"CanInvite", store.CanInvite},
		{"CanReconnect", store.CanReconnect},
	}

	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			ok, err := c.fn(ctx, rs, setup.channelID, setup.creatorID)
			require.NoError(t, err)
			assert.True(t, ok, "a live creator row must be authorized")

			ok, err = c.fn(ctx, rs, setup.channelID, setup.analystID)
			require.NoError(t, err)
			assert.False(t, ok, "an analyst must not be authorized for a creator-only check")

			ok, err = c.fn(ctx, rs, setup.channelID, setup.unassociatedID)
			require.NoError(t, err)
			assert.False(t, ok, "a Person with no row on the Channel must not be authorized")

			ok, err = c.fn(ctx, rs, setup.channelID, setup.formerID)
			require.NoError(t, err)
			assert.False(t, ok, "a Person whose creator row has been closed (valid_to set) must not be "+
				"authorized -- proves the check reads the join table live, not a static owner field")
		})
	}
}

func TestAuthz_CreatorOrAnalystChecks_AgainstRealRoleStore(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	setup := setupChannelWithRoles(t, ctx, s, db)
	rs := s.Roles()

	checks := []struct {
		name string
		fn   func(context.Context, store.RoleStore, uuid.UUID, uuid.UUID) (bool, error)
	}{
		{"CanRead", store.CanRead},
		{"CanWrite", store.CanWrite},
	}

	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			ok, err := c.fn(ctx, rs, setup.channelID, setup.creatorID)
			require.NoError(t, err)
			assert.True(t, ok, "creator must be authorized")

			ok, err = c.fn(ctx, rs, setup.channelID, setup.analystID)
			require.NoError(t, err)
			assert.True(t, ok, "analyst must be authorized")

			ok, err = c.fn(ctx, rs, setup.channelID, setup.unassociatedID)
			require.NoError(t, err)
			assert.False(t, ok, "an unassociated Person must not be authorized")
		})
	}
}

// ── InviteStore (FR5-FR8) ──────────────────────────────────────────────────

func TestInviteStore_Generate_TwiceLeavesExactlyOneLiveCode(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)

	creator, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-inv-creator", "c@example.com", "Creator")
	require.NoError(t, err)
	ch, err := s.Channels().Create(ctx, "yt-inv-1", "Channel", creator.ID)
	require.NoError(t, err)

	inv1, err := s.Invites().Generate(ctx, ch.ID, creator.ID)
	require.NoError(t, err)
	assert.Nil(t, inv1.InvalidatedAt)

	inv2, err := s.Invites().Generate(ctx, ch.ID, creator.ID)
	require.NoError(t, err)
	assert.Nil(t, inv2.InvalidatedAt)
	assert.NotEqual(t, inv1.Code, inv2.Code)

	got1, err := s.Invites().Lookup(ctx, inv1.Code)
	require.NoError(t, err)
	assert.NotNil(t, got1.InvalidatedAt, "generating a second invite must invalidate the first (FR5)")

	var liveCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM channel_invite
		WHERE channel_id = $1 AND consumed_at IS NULL AND invalidated_at IS NULL
	`, ch.ID).Scan(&liveCount))
	assert.Equal(t, 1, liveCount, "at most one live code per Channel")

	err = s.Invites().Consume(ctx, inv1.Code, creator.ID)
	assert.ErrorIs(t, err, store.ErrInviteInvalidated, "consuming an invalidated code must fail (FR8)")
}

func TestInviteStore_Consume_GrantsAnalystRole_SecondConsumeFailsAndAddsNoRow(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)

	creator, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-consume-creator", "c@example.com", "Creator")
	require.NoError(t, err)
	ch, err := s.Channels().Create(ctx, "yt-consume-1", "Channel", creator.ID)
	require.NoError(t, err)

	invitee, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-invitee", "i@example.com", "Invitee")
	require.NoError(t, err)

	inv, err := s.Invites().Generate(ctx, ch.ID, creator.ID)
	require.NoError(t, err)

	require.NoError(t, s.Invites().Consume(ctx, inv.Code, invitee.ID))

	got, err := s.Invites().Lookup(ctx, inv.Code)
	require.NoError(t, err)
	require.NotNil(t, got.ConsumedAt)
	require.NotNil(t, got.ConsumedByPersonID)
	assert.Equal(t, invitee.ID, *got.ConsumedByPersonID)

	roles, err := s.Roles().RolesFor(ctx, ch.ID, invitee.ID)
	require.NoError(t, err)
	assert.Equal(t, []store.Role{store.RoleAnalyst}, roles, "Consume must grant exactly one live analyst role (FR8)")

	err = s.Invites().Consume(ctx, inv.Code, invitee.ID)
	assert.ErrorIs(t, err, store.ErrInviteConsumed, "a second consume of an already-consumed code must fail")

	var roleRowCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM channel_person WHERE channel_id = $1 AND person_id = $2
	`, ch.ID, invitee.ID).Scan(&roleRowCount))
	assert.Equal(t, 1, roleRowCount, "a failed second consume must not add a duplicate role row")
}

// ── IdeaStore / VerdictStore (FR9, FR12/FR13) ──────────────────────────────

func TestVerdictStore_Append_TwiceYieldsTwoVersions_FirstRowUnchanged(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	idea, err := s.Ideas().Create(ctx, ch.ID, "Cats vs Dogs Reaction", creator.ID)
	require.NoError(t, err)

	v1, err := s.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictNeedsMoreResearch,
		Reasoning: "not enough comps yet", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, v1.Version)

	v2, err := s.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable,
		Reasoning: "comps look strong now", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, v2.Version, "Append must allocate version = max+1 (FR12)")
	assert.NotEqual(t, v1.ID, v2.ID)

	current, err := s.Verdicts().Current(ctx, idea.ID)
	require.NoError(t, err)
	assert.Equal(t, v2, current, "Current must return the highest-version verdict")

	history, err := s.Verdicts().History(ctx, idea.ID)
	require.NoError(t, err)
	require.Len(t, history, 2, "History must return both versions (FR13)")
	assert.Equal(t, v1, history[0], "the version-1 row must be byte-identical after a later Append (FR12 append-only)")
	assert.Equal(t, v2, history[1])
	assert.True(t, history[0].Version < history[1].Version, "History must be ordered by version ascending")
}

// ── ScheduleStore.SaveDraft (FR16, LB3) ─────────────────────────────────────

func TestScheduleStore_SaveDraft_RejectsNonViableVerdict_AcceptsViable(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	idea, err := s.Ideas().Create(ctx, ch.ID, "Idea Needing Judgement", creator.ID)
	require.NoError(t, err)

	notViable, err := s.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictNotViable, Reasoning: "saturated niche", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	_, err = s.Schedules().SaveDraft(ctx, store.SaveDraftInput{
		ChannelID: ch.ID, IdeaID: idea.ID, VerdictID: notViable.ID,
		ProposedPublishAt: time.Now().Add(24 * time.Hour), CreatedByPersonID: creator.ID,
	})
	assert.ErrorIs(t, err, store.ErrVerdictNotViable, "SaveDraft must reject a not-viable verdict (FR16)")

	needsMore, err := s.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictNeedsMoreResearch, Reasoning: "more comps needed", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	_, err = s.Schedules().SaveDraft(ctx, store.SaveDraftInput{
		ChannelID: ch.ID, IdeaID: idea.ID, VerdictID: needsMore.ID,
		ProposedPublishAt: time.Now().Add(24 * time.Hour), CreatedByPersonID: creator.ID,
	})
	assert.ErrorIs(t, err, store.ErrVerdictNotViable, "SaveDraft must reject a needs-more-research verdict (FR16)")

	viable, err := s.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "green light", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	entry, err := s.Schedules().SaveDraft(ctx, store.SaveDraftInput{
		ChannelID: ch.ID, IdeaID: idea.ID, VerdictID: viable.ID,
		ProposedPublishAt: time.Now().Add(24 * time.Hour), CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err, "SaveDraft must accept a viable verdict (FR16)")
	assert.Equal(t, viable.ID, entry.VerdictID, "the draft must store the exact verdict_id that judged the idea viable (LB3)")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM schedule_entry WHERE idea_id = $1`, idea.ID).Scan(&count))
	assert.Equal(t, 1, count, "the two rejected SaveDraft calls must not have written any schedule_entry row")
}

// ── ResearchStore (FR9/FR10) ────────────────────────────────────────────────

func TestResearchStore_SaveNote_UncitedAndCitedRoundTripDistinctly(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	uncited, err := s.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, Text: "gut feeling, no source", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	assert.Nil(t, uncited.SourceURL, "a note saved with no SourceURL must round-trip as nil, not empty string (FR10)")

	url := "https://example.com/trend-report"
	cited, err := s.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: ch.ID, Text: "trend report says so", SourceURL: &url, AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	require.NotNil(t, cited.SourceURL)
	assert.Equal(t, url, *cited.SourceURL)

	notes, err := s.Research().ListByChannel(ctx, ch.ID)
	require.NoError(t, err)
	require.Len(t, notes, 2)

	byID := map[uuid.UUID]store.ResearchNote{notes[0].ID: notes[0], notes[1].ID: notes[1]}
	assert.Nil(t, byID[uncited.ID].SourceURL, "the read model must distinguish the uncited note")
	require.NotNil(t, byID[cited.ID].SourceURL)
	assert.Equal(t, url, *byID[cited.ID].SourceURL, "the read model must distinguish the cited note")
}

// ── PacingStore (FR17, NFR2) ────────────────────────────────────────────────

func TestPacingStore_Upsert_TwiceWithIdenticalValuesLeavesOneRow(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	policy := store.PacingPolicy{
		TargetUploadsPerWeek: 3,
		PreferredDays:        []string{"mon", "wed", "fri"},
		UpdatedByPersonID:    creator.ID,
	}

	first, err := s.Pacing().Upsert(ctx, ch.ID, policy)
	require.NoError(t, err)

	second, err := s.Pacing().Upsert(ctx, ch.ID, policy)
	require.NoError(t, err)

	assert.Equal(t, first.ID, second.ID, "repeated Upsert with identical values must converge on the same row (NFR2)")
	assert.Equal(t, policy.TargetUploadsPerWeek, second.TargetUploadsPerWeek)
	assert.Equal(t, policy.PreferredDays, second.PreferredDays)

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM pacing_policy WHERE channel_id = $1`, ch.ID).Scan(&count))
	assert.Equal(t, 1, count, "must leave exactly one row, not one per Upsert call")
}

// ── SyncStore (FR14/FR21) ────────────────────────────────────────────────────

func TestSyncStore_UpsertVideos_SameYouTubeIDUpdatesNotDuplicates(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, _ := setupChannel(t, ctx, s)

	err := s.Sync().UpsertVideos(ctx, ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: "yt-video-1", Title: "Draft Title",
		PrivacyStatus: store.PrivacyStatusPrivate, IsScheduledDraft: true, LastSyncedAt: time.Now(),
	}})
	require.NoError(t, err)

	err = s.Sync().UpsertVideos(ctx, ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: "yt-video-1", Title: "Published Title",
		PrivacyStatus: store.PrivacyStatusPublic, IsScheduledDraft: false, LastSyncedAt: time.Now(),
	}})
	require.NoError(t, err, "a second sync of the same youtube_video_id must update, not error")

	videos, err := s.Sync().ListSchedule(ctx, ch.ID)
	require.NoError(t, err)
	require.Len(t, videos, 1, "must not duplicate the row")
	assert.Equal(t, "Published Title", videos[0].Title, "the second upsert must have updated the existing row's fields")
	assert.Equal(t, store.PrivacyStatusPublic, videos[0].PrivacyStatus)

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM synced_video WHERE channel_id = $1 AND youtube_video_id = $2`, ch.ID, "yt-video-1",
	).Scan(&count))
	assert.Equal(t, 1, count)
}

// ── MatchStore.Resolve (FR22/FR23, issue #1581) ─────────────────────────────

// setupPendingMatch creates a Channel/creator, a committed schedule_entry,
// a published synced_video, and one MatchStatePending video_schedule_match
// row linking them (as if the matcher had queued it below threshold) --
// the fixture every Resolve test below starts from.
func setupPendingMatch(t *testing.T, ctx context.Context, s *store.Store) (ch store.Channel, creator store.Person, entry store.ScheduleEntry, video store.SyncedVideo, match store.VideoScheduleMatch) {
	t.Helper()

	ch, creator = setupChannel(t, ctx, s)

	idea, err := s.Ideas().Create(ctx, ch.ID, "Resolve Test Idea", creator.ID)
	require.NoError(t, err)
	verdict, err := s.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "greenlit", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	entry, err = s.Schedules().SaveDraft(ctx, store.SaveDraftInput{
		ChannelID: ch.ID, IdeaID: idea.ID, VerdictID: verdict.ID,
		ProposedPublishAt: time.Now().Add(24 * time.Hour), CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)
	require.NoError(t, s.Schedules().Approve(ctx, entry.ID, creator.ID))

	require.NoError(t, s.Sync().UpsertVideos(ctx, ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: "yt-resolve-" + uuid.NewString(), Title: "A Video",
		PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: ptrTime(time.Now()), LastSyncedAt: time.Now(),
	}}))
	synced, err := s.Sync().ListSchedule(ctx, ch.ID)
	require.NoError(t, err)
	require.Len(t, synced, 1)
	video = synced[0]

	require.NoError(t, s.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, ScheduleEntryID: &entry.ID, Confidence: 0.5, State: store.MatchStatePending,
	}))
	pending, err := s.Matches().ListPending(ctx, ch.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	match = pending[0]

	return ch, creator, entry, video, match
}

func TestMatchStore_Resolve_Confirm_SetsConfirmedLinkAndResolver(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	_, creator, entry, _, match := setupPendingMatch(t, ctx, s)

	require.NoError(t, s.Matches().Resolve(ctx, match.ID, creator.ID, true, nil))

	got, err := s.Matches().GetByID(ctx, match.ID)
	require.NoError(t, err)
	assert.Equal(t, store.MatchStateConfirmed, got.State)
	require.NotNil(t, got.ScheduleEntryID)
	assert.Equal(t, entry.ID, *got.ScheduleEntryID, "confirming with a nil override must keep the original best-guess entry")
	require.NotNil(t, got.ResolvedByPersonID)
	assert.Equal(t, creator.ID, *got.ResolvedByPersonID)
	require.NotNil(t, got.ResolvedAt)
}

func TestMatchStore_Resolve_ConfirmWithOverrideEntryID_LinksToChosenEntryNotBestGuess(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch, creator, _, _, match := setupPendingMatch(t, ctx, s)

	otherIdea, err := s.Ideas().Create(ctx, ch.ID, "A Different Idea", creator.ID)
	require.NoError(t, err)
	otherVerdict, err := s.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: otherIdea.ID, Verdict: store.VerdictViable, Reasoning: "also greenlit", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	otherEntry, err := s.Schedules().SaveDraft(ctx, store.SaveDraftInput{
		ChannelID: ch.ID, IdeaID: otherIdea.ID, VerdictID: otherVerdict.ID,
		ProposedPublishAt: time.Now().Add(48 * time.Hour), CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)
	require.NoError(t, s.Schedules().Approve(ctx, otherEntry.ID, creator.ID))

	require.NoError(t, s.Matches().Resolve(ctx, match.ID, creator.ID, true, &otherEntry.ID))

	got, err := s.Matches().GetByID(ctx, match.ID)
	require.NoError(t, err)
	assert.Equal(t, store.MatchStateConfirmed, got.State)
	require.NotNil(t, got.ScheduleEntryID)
	assert.Equal(t, otherEntry.ID, *got.ScheduleEntryID, "an explicit override must replace the matcher's original best guess")
}

func TestMatchStore_Resolve_Reject_LeavesScheduleEntryIDUntouchedVideoUnmatched(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	_, creator, entry, _, match := setupPendingMatch(t, ctx, s)

	require.NoError(t, s.Matches().Resolve(ctx, match.ID, creator.ID, false, nil))

	got, err := s.Matches().GetByID(ctx, match.ID)
	require.NoError(t, err)
	assert.Equal(t, store.MatchStateRejected, got.State)
	// Resolve's contract: a reject leaves schedule_entry_id exactly as
	// Record originally wrote it (here, the matcher's best guess) -- FR23's
	// "the video remains unmatched" is enforced by v_prediction_vs_outcome
	// only ever joining state IN ('auto','confirmed'), not by nulling this
	// column out.
	require.NotNil(t, got.ScheduleEntryID)
	assert.Equal(t, entry.ID, *got.ScheduleEntryID)
	require.NotNil(t, got.ResolvedByPersonID)
	assert.Equal(t, creator.ID, *got.ResolvedByPersonID)
}

func TestMatchStore_Resolve_AlreadyResolved_ReturnsErrMatchNotPending_NoSilentStateFlip(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	_, creator, _, _, match := setupPendingMatch(t, ctx, s)

	require.NoError(t, s.Matches().Resolve(ctx, match.ID, creator.ID, true, nil))
	firstResolve, err := s.Matches().GetByID(ctx, match.ID)
	require.NoError(t, err)

	// A second resolution attempt -- even the opposite direction (reject) --
	// must be rejected as a conflict, never silently re-flip the state.
	err = s.Matches().Resolve(ctx, match.ID, creator.ID, false, nil)
	assert.ErrorIs(t, err, store.ErrMatchNotPending)

	secondRead, err := s.Matches().GetByID(ctx, match.ID)
	require.NoError(t, err)
	assert.Equal(t, firstResolve.State, secondRead.State, "state must be unchanged after a rejected re-resolution attempt")
	assert.Equal(t, firstResolve.ScheduleEntryID, secondRead.ScheduleEntryID)
	assert.Equal(t, firstResolve.ResolvedAt, secondRead.ResolvedAt)
}

func TestMatchStore_Resolve_NonexistentMatchID_ReturnsErrMatchNotPending(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	_, creator := setupChannel(t, ctx, s)

	err := s.Matches().Resolve(ctx, uuid.New(), creator.ID, true, nil)
	assert.ErrorIs(t, err, store.ErrMatchNotPending, "resolving a match id that does not exist at all must be the same error as resolving an already-resolved one, never a distinct not-found")
}

// ── Idempotency (NFR2/LB4) ───────────────────────────────────────────────────

func TestIdempotency_Do_ReplayConflictAndDistinctKeySemantics(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	_, creator := setupChannel(t, ctx, s)

	idem := s.Idempotency()
	var calls int
	fn := func(context.Context) (uuid.UUID, error) {
		calls++
		return uuid.New(), nil
	}

	first, replayed, err := idem.Do(ctx, "save_research_note", creator.ID, "key-1", "fp-1", fn)
	require.NoError(t, err)
	assert.False(t, replayed)
	assert.Equal(t, 1, calls)

	second, replayed, err := idem.Do(ctx, "save_research_note", creator.ID, "key-1", "fp-1", fn)
	require.NoError(t, err)
	assert.True(t, replayed, "same key + same fingerprint must replay")
	assert.Equal(t, first, second, "a replay must return the prior result_ref unchanged")
	assert.Equal(t, 1, calls, "a replay must not run fn a second time")

	_, _, err = idem.Do(ctx, "save_research_note", creator.ID, "key-1", "fp-2", fn)
	assert.ErrorIs(t, err, store.ErrIdempotencyConflict, "same key + different fingerprint must conflict")
	assert.Equal(t, 1, calls, "a conflict must not run fn")

	third, replayed, err := idem.Do(ctx, "save_research_note", creator.ID, "key-2", "fp-1", fn)
	require.NoError(t, err)
	assert.False(t, replayed, "a different key must run fn again")
	assert.Equal(t, 2, calls)
	assert.NotEqual(t, first, third)
}

// ── v_prediction_vs_outcome (FR24, M3's C14 aggregate surface) ──────────────

// predictionVsOutcomeRow reads exactly v_prediction_vs_outcome's column
// list -- store.PredictionVsOutcome has no dedicated store method yet (only
// the model type exists, migration 002), so this test queries the view
// directly, the same way store_integration_test.go elsewhere reaches past
// the store API to assert on raw table/view state.
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

func TestPredictionVsOutcomeView_OnlyMatchedPublishedCommittedIdeasAppear(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	// ideaMatched: the full chain -- viable verdict, committed schedule
	// entry, a confirmed match to a synced video, and metrics. Must appear.
	ideaMatched, err := s.Ideas().Create(ctx, ch.ID, "Matched Idea", creator.ID)
	require.NoError(t, err)
	verdictMatched, err := s.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: ideaMatched.ID, Verdict: store.VerdictViable, Reasoning: "strong comps", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	entryMatched, err := s.Schedules().SaveDraft(ctx, store.SaveDraftInput{
		ChannelID: ch.ID, IdeaID: ideaMatched.ID, VerdictID: verdictMatched.ID,
		ProposedPublishAt: time.Now().Add(24 * time.Hour), CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)
	require.NoError(t, s.Schedules().Approve(ctx, entryMatched.ID, creator.ID))

	require.NoError(t, s.Sync().UpsertVideos(ctx, ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: "yt-matched", Title: "Matched Video",
		PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: ptrTime(time.Now()), LastSyncedAt: time.Now(),
	}}))
	synced, err := s.Sync().ListSchedule(ctx, ch.ID)
	require.NoError(t, err)
	require.Len(t, synced, 1)
	matchedVideoID := synced[0].ID

	require.NoError(t, s.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: matchedVideoID, ScheduleEntryID: &entryMatched.ID,
		Confidence: 0.97, State: store.MatchStateConfirmed,
	}))
	require.NoError(t, s.Sync().UpsertMetrics(ctx, []store.VideoMetrics{{
		SyncedVideoID: matchedVideoID, Views: ptrInt64(1234), MeasuredAt: time.Now(),
	}}))

	// ideaDraftOnly: viable verdict, but the schedule entry is never
	// approved -- no committed entry, must NOT appear.
	ideaDraftOnly, err := s.Ideas().Create(ctx, ch.ID, "Draft Only Idea", creator.ID)
	require.NoError(t, err)
	verdictDraftOnly, err := s.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: ideaDraftOnly.ID, Verdict: store.VerdictViable, Reasoning: "also strong", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	_, err = s.Schedules().SaveDraft(ctx, store.SaveDraftInput{
		ChannelID: ch.ID, IdeaID: ideaDraftOnly.ID, VerdictID: verdictDraftOnly.ID,
		ProposedPublishAt: time.Now().Add(24 * time.Hour), CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	// ideaUnmatched: committed schedule entry, but no video_schedule_match
	// at all -- must NOT appear.
	ideaUnmatched, err := s.Ideas().Create(ctx, ch.ID, "Unmatched Idea", creator.ID)
	require.NoError(t, err)
	verdictUnmatched, err := s.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: ideaUnmatched.ID, Verdict: store.VerdictViable, Reasoning: "strong too", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	entryUnmatched, err := s.Schedules().SaveDraft(ctx, store.SaveDraftInput{
		ChannelID: ch.ID, IdeaID: ideaUnmatched.ID, VerdictID: verdictUnmatched.ID,
		ProposedPublishAt: time.Now().Add(24 * time.Hour), CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)
	require.NoError(t, s.Schedules().Approve(ctx, entryUnmatched.ID, creator.ID))

	rows := predictionVsOutcomeRowsForChannel(t, ctx, db, ch.ID)
	require.Len(t, rows, 1, "only the fully matched-and-published idea must appear")
	assert.Equal(t, ideaMatched.ID, rows[0].IdeaID)
	assert.Equal(t, "Matched Idea", rows[0].IdeaTitle)
	assert.Equal(t, verdictMatched.ID, rows[0].VerdictID)
	assert.Equal(t, store.VerdictViable, rows[0].Verdict)
	assert.Equal(t, "strong comps", rows[0].VerdictReasoning, "the view must carry the verdict text alongside the outcome")
	require.NotNil(t, rows[0].Views)
	assert.Equal(t, int64(1234), *rows[0].Views, "the view must carry the metrics alongside the verdict")
}

func ptrTime(t time.Time) *time.Time { return &t }
func ptrInt64(v int64) *int64        { return &v }

// ── Migration reversibility (001 + 002 + 003 + 004 + 005 + 006 + 007) ──────

// TestMigrations_UpDownUp_LeavesNoOrphanObjects exercises schema.Migrations'
// full up/down/up cycle across every embedded migration, not just one in
// isolation -- it was written against 001 alone (#1568), extended for 002
// (#1569), extended again for 003 (web_session, #1570), again for 005
// (mcp_credential, #1575), again for 006 (mcp_credential dropped and
// recreated against libs/go/mcpauth's schema contract, #1643), and again
// for 007 (mcp_oauth_client + mcp_auth_code, mcpauth's OAuth2
// authorization-code + PKCE front end, #1646), so the version assertion and
// table list below cover all of them rather than any single one.
func TestMigrations_UpDownUp_LeavesNoOrphanObjects(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "first up")
	require.NoError(t, runner.Down(), "down")
	require.NoError(t, runner.Up(), "second up")

	version, dirty, err := runner.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(7), version, "highest migration in schema.Migrations is 007_mcpauth_oauth")

	for _, tbl := range []string{
		"person", "channel", "channel_person", "channel_invite",
		"idea", "research_note", "viability_verdict", "verdict_citation",
		"pacing_policy", "schedule_entry", "synced_video", "video_metrics",
		"video_schedule_match", "mcp_idempotency",
		"web_session",
		"channel_credential",
		"mcp_credential",
		"mcp_oauth_client",
		"mcp_auth_code",
	} {
		var exists bool
		require.NoError(t, db.Pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`, tbl,
		).Scan(&exists))
		assert.True(t, exists, "table %s must exist after up/down/up", tbl)
	}

	// Migration 002's views must also survive the down/up cycle -- the down
	// migration drops them explicitly (DROP VIEW IF EXISTS) before dropping
	// their underlying tables, so a partial down/up that forgot to re-drop
	// or re-create a view would surface here as a missing or duplicate
	// object.
	for _, view := range []string{"v_current_verdict", "v_prediction_vs_outcome"} {
		var exists bool
		require.NoError(t, db.Pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.views WHERE table_name = $1)`, view,
		).Scan(&exists))
		assert.True(t, exists, "view %s must exist after up/down/up", view)
	}

	// A fresh insert must succeed cleanly, proving indexes/constraints
	// (e.g. the person.google_subject UNIQUE index) survived the
	// down/up cycle intact rather than silently not being recreated.
	_, err = db.Pool.Exec(ctx, `INSERT INTO person (google_subject) VALUES ($1)`, "up-down-up-check")
	require.NoError(t, err, "insert after up/down/up must succeed against a fully-recreated schema")
}

// TestMigration003_WebSessionTable_ConstraintsSurviveDownUp proves migration
// 003's own constraints -- session_id PRIMARY KEY and the person_id FOREIGN
// KEY -- are actually created and survive a down/up cycle (not just that the
// table exists, which the test above already covers).
func TestMigration003_WebSessionTable_ConstraintsSurviveDownUp(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "first up")
	require.NoError(t, runner.Down(), "down")
	require.NoError(t, runner.Up(), "second up")

	// FOREIGN KEY: a web_session row referencing a person_id that does not
	// exist must be rejected.
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO web_session (session_id, person_id, expires_at)
		VALUES ('sess-orphan', $1, NOW() + interval '1 hour')
	`, uuid.New())
	assert.Error(t, err, "web_session.person_id FOREIGN KEY must reject a nonexistent person")

	// PRIMARY KEY: a second row with the same session_id must be rejected.
	var personID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO person (google_subject) VALUES ($1) RETURNING id
	`, "sub-web-session-pk-check").Scan(&personID))

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO web_session (session_id, person_id, expires_at)
		VALUES ('sess-dup', $1, NOW() + interval '1 hour')
	`, personID)
	require.NoError(t, err, "first insert with a fresh session_id must succeed")

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO web_session (session_id, person_id, expires_at)
		VALUES ('sess-dup', $1, NOW() + interval '1 hour')
	`, personID)
	assert.Error(t, err, "web_session.session_id PRIMARY KEY must reject a duplicate session_id")
}

// TestMigration006_McpCredentialTable_ConstraintsAndIndexesSurviveDownUp
// machine-checks (rather than eyeballs) the two things FR13/NFR5 require
// migration 006 preserve from 005's mcp_credential shape even though the
// table is now built against mcpauth's generic schema contract: the
// person_id FOREIGN KEY (mcpauth itself treats identity as an opaque
// string -- ASS's own FK is layered on top and must not get lost) and both
// indexes named in 006's SQL. TestMigrations_UpDownUp_LeavesNoOrphanObjects
// above only proves the table itself exists after down/up; this proves its
// constraints do too.
func TestMigration006_McpCredentialTable_ConstraintsAndIndexesSurviveDownUp(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "first up")
	require.NoError(t, runner.Down(), "down")
	require.NoError(t, runner.Up(), "second up")

	// FOREIGN KEY: a mcp_credential row referencing a person_id that does
	// not exist must be rejected -- mcpauth's own CredentialStore never
	// exercises this (it only ever binds identities the caller already
	// validated), so it can only be proven with a direct SQL insert like
	// this one.
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO mcp_credential (person_id, token_hash)
		VALUES ($1, 'deadbeef')
	`, uuid.New())
	assert.Error(t, err, "mcp_credential.person_id FOREIGN KEY must reject a nonexistent person")

	var personID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO person (google_subject) VALUES ($1) RETURNING id
	`, "sub-mcp-credential-fk-check").Scan(&personID))

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO mcp_credential (person_id, token_hash)
		VALUES ($1, 'deadbeef')
	`, personID)
	require.NoError(t, err, "a mcp_credential row referencing a real person must be accepted")

	// UNIQUE: a second row reusing the same token_hash must be rejected --
	// both 005 and 006 declare token_hash TEXT NOT NULL UNIQUE.
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO mcp_credential (person_id, token_hash)
		VALUES ($1, 'deadbeef')
	`, personID)
	assert.Error(t, err, "mcp_credential.token_hash UNIQUE must reject a duplicate hash")

	// Both indexes 006's SQL names must actually exist, not merely be
	// implied by the UNIQUE constraint above.
	for _, idx := range []string{"mcp_credential_token_hash", "mcp_credential_person_id"} {
		var exists bool
		require.NoError(t, db.Pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE tablename = 'mcp_credential' AND indexname = $1)`, idx,
		).Scan(&exists))
		assert.True(t, exists, "index %s must exist on mcp_credential after up/down/up", idx)
	}
}
