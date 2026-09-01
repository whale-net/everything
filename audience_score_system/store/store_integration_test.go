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
// migration 001 from the package's own embedded schema (schema.Migrations),
// and returns a ready *store.Store plus the underlying dbtest.Postgres for
// tests that need to reach past the store's own API (e.g. to assert on row
// counts directly, or to close a channel_person row out from under the
// store to prove authz reads live state).
func newStore(t *testing.T) (*store.Store, *dbtest.Postgres) {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply migration 001 from the real embedded schema")

	return store.New(db.Pool), db
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
	channelID                                     uuid.UUID
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

// ── Migration reversibility (001 + 003) ────────────────────────────────────

// TestMigration001_UpDownUp_LeavesNoOrphanObjects exercises schema.Migrations'
// full up/down/up cycle -- not just migration 001 in isolation. It was
// written against migration 001 alone (#1568); migration 003 (web_session,
// #1570) landed afterward in the same embedded schema.Migrations FS, so
// runner.Up() now advances to version 3, not 1 -- the version assertion and
// table list below were updated accordingly rather than left asserting a
// version number schema.Migrations can no longer produce.
func TestMigration001_UpDownUp_LeavesNoOrphanObjects(t *testing.T) {
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
	assert.Equal(t, uint(3), version, "highest migration in schema.Migrations is 003_web_session")

	for _, tbl := range []string{"person", "channel", "channel_person", "channel_invite", "web_session"} {
		var exists bool
		require.NoError(t, db.Pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`, tbl,
		).Scan(&exists))
		assert.True(t, exists, "table %s must exist after up/down/up", tbl)
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
