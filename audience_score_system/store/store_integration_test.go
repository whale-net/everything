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
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

// TestChannelStore_Create_RecordsFounderSelfGrant extends
// TestChannelStore_Create_CreatesExactlyOneLiveCreatorRow above: it also
// asserts on granted_by_person_id (FR25/FR34), which that test does not
// touch.
func TestChannelStore_Create_RecordsFounderSelfGrant(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)

	creator, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-founder-self-grant", "f@example.com", "Founder")
	require.NoError(t, err)

	ch, err := s.Channels().Create(ctx, "yt-founder-self-grant", "My Channel", creator.ID)
	require.NoError(t, err)

	var grantedBy uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT granted_by_person_id FROM channel_person
		WHERE channel_id = $1 AND person_id = $2 AND valid_to IS NULL
	`, ch.ID, creator.ID).Scan(&grantedBy))
	assert.Equal(t, creator.ID, grantedBy, "the connecting Person must be recorded as their own granted_by_person_id (FR25 self-grant at connect)")
}

// ── authz.go against the real SQL-backed RoleStore ─────────────────────────

// channelWithRoles sets up one Channel with a live creator, a live analyst,
// a Person with no row at all, and a Person whose creator role on that same
// Channel has been explicitly closed (valid_to stamped) -- the case that
// proves an authz check reads live join-table state rather than a
// static/cached field.
//
// The "former creator" is granted and then revoked on a *second*, throwaway
// Channel rather than the main one: migration 009's NFR10 partial unique
// index (channel_person_channel_id_founder_current) enforces at most one
// *open* role=creator row per channel_id at any instant, and the main
// Channel's own creator row (from Channels().Create below) is already open
// for the whole lifetime of this fixture. Granting a second, simultaneous
// open creator row to `former` on that same channel_id would violate the
// index at INSERT time -- correctly so, per FR29/NFR10, since two live
// Founders on one Channel was never a valid scenario. A dedicated channel
// for the revoke-then-check sequence preserves this fixture's actual
// intent (a closed creator row on the channel being checked must not
// authorize) without ever creating two live creators on one Channel.
type channelWithRoles struct {
	channelID, formerChannelID                     uuid.UUID
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
	require.NoError(t, s.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst, creator.ID))

	unassociated, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "unassoc@example.com", "Unassociated")
	require.NoError(t, err)

	former, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "former@example.com", "Former Creator")
	require.NoError(t, err)
	// Own Channel (see channelWithRoles doc comment for why) -- Create
	// itself grants role=creator to `former`, so no separate AddRole call
	// is needed here.
	formerCh, err := s.Channels().Create(ctx, "yt-"+uuid.NewString(), "Former Channel", former.ID)
	require.NoError(t, err)
	// Close the row directly (bypassing the store API, which has no
	// "revoke" method yet) to simulate a Person whose creator role has
	// lapsed -- proves the Can* checks read valid_to IS NULL state, not a
	// static owner field (NFR5).
	_, err = db.Pool.Exec(ctx, `
		UPDATE channel_person SET valid_to = NOW()
		WHERE channel_id = $1 AND person_id = $2 AND valid_to IS NULL
	`, formerCh.ID, former.ID)
	require.NoError(t, err)

	return channelWithRoles{
		channelID:       ch.ID,
		formerChannelID: formerCh.ID,
		creatorID:       creator.ID,
		analystID:       analyst.ID,
		unassociatedID:  unassociated.ID,
		formerID:        former.ID,
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

			ok, err = c.fn(ctx, rs, setup.formerChannelID, setup.formerID)
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

// TestAuthz_CoCreator_HasCreatorTierAuthority_AgainstRealRoleStore proves a
// real role=co_creator row (granted via RoleStore.AddRole, not a fake) is
// enough to authorize approve/invite/reconnect/read/write -- Founder and
// Co-Creator hold symmetric authority (FR32) against the real SQL-backed
// RoleStore, not just the pure-Go fake in authz_test.go.
func TestAuthz_CoCreator_HasCreatorTierAuthority_AgainstRealRoleStore(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch, creator := setupChannel(t, ctx, s)
	rs := s.Roles()

	coCreator, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "cc@example.com", "Co-Creator")
	require.NoError(t, err)
	require.NoError(t, s.Roles().AddRole(ctx, ch.ID, coCreator.ID, store.RoleCoCreator, creator.ID))

	checks := []struct {
		name string
		fn   func(context.Context, store.RoleStore, uuid.UUID, uuid.UUID) (bool, error)
	}{
		{"CanApprove", store.CanApprove},
		{"CanInvite", store.CanInvite},
		{"CanReconnect", store.CanReconnect},
		{"CanRead", store.CanRead},
		{"CanWrite", store.CanWrite},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			ok, err := c.fn(ctx, rs, ch.ID, coCreator.ID)
			require.NoError(t, err)
			assert.True(t, ok, "a real co_creator row must authorize %s", c.name)
		})
	}
}

// ── InviteStore (FR5-FR8) ──────────────────────────────────────────────────

// TestInviteStore_Generate_SameTierTwice_ReturnsSameLiveCode is the M2
// rewrite of the M1 test with this behavior: FR30 replaces "regenerate
// invalidates the prior code" with "regenerate returns the existing live
// code" -- the same tier's second Generate call must hand back inv1
// unchanged, not mint and invalidate-replace it.
func TestInviteStore_Generate_SameTierTwice_ReturnsSameLiveCode(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)

	creator, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-inv-creator", "c@example.com", "Creator")
	require.NoError(t, err)
	ch, err := s.Channels().Create(ctx, "yt-inv-1", "Channel", creator.ID)
	require.NoError(t, err)

	inv1, err := s.Invites().Generate(ctx, ch.ID, creator.ID, store.RoleAnalyst)
	require.NoError(t, err)
	assert.Nil(t, inv1.InvalidatedAt)

	inv2, err := s.Invites().Generate(ctx, ch.ID, creator.ID, store.RoleAnalyst)
	require.NoError(t, err)
	assert.Nil(t, inv2.InvalidatedAt)
	assert.Equal(t, inv1.Code, inv2.Code, "FR30: a repeat Generate for the same tier must return the SAME live code")
	assert.Equal(t, inv1.ID, inv2.ID)

	got1, err := s.Invites().Lookup(ctx, inv1.Code)
	require.NoError(t, err)
	assert.Nil(t, got1.InvalidatedAt, "the existing live code must not be invalidated by a repeat generate")

	var liveCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM channel_invite
		WHERE channel_id = $1 AND consumed_at IS NULL AND invalidated_at IS NULL
	`, ch.ID).Scan(&liveCount))
	assert.Equal(t, 1, liveCount, "at most one live code per (Channel, tier)")

	require.NoError(t, s.Invites().Consume(ctx, inv1.Code, creator.ID))
}

// TestInviteStore_Generate_BothTiers_TwoLiveCodesCoexist proves NFR11's
// rescoped uniqueness index and Generate's per-tier idempotency compose
// correctly: an Analyst invite and a Co-Creator invite on the same
// Channel are distinct, both live, and both independently consumable.
func TestInviteStore_Generate_BothTiers_TwoLiveCodesCoexist(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	analystInv, err := s.Invites().Generate(ctx, ch.ID, creator.ID, store.RoleAnalyst)
	require.NoError(t, err)
	coCreatorInv, err := s.Invites().Generate(ctx, ch.ID, creator.ID, store.RoleCoCreator)
	require.NoError(t, err)

	assert.NotEqual(t, analystInv.Code, coCreatorInv.Code)
	assert.Equal(t, store.RoleAnalyst, analystInv.Role)
	assert.Equal(t, store.RoleCoCreator, coCreatorInv.Role)

	analyst, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "a@example.com", "Analyst")
	require.NoError(t, err)
	require.NoError(t, s.Invites().Consume(ctx, analystInv.Code, analyst.ID))

	coCreator, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "cc@example.com", "Co-Creator")
	require.NoError(t, err)
	require.NoError(t, s.Invites().Consume(ctx, coCreatorInv.Code, coCreator.ID))

	analystRoles, err := s.Roles().RolesFor(ctx, ch.ID, analyst.ID)
	require.NoError(t, err)
	assert.Equal(t, []store.Role{store.RoleAnalyst}, analystRoles)

	coCreatorRoles, err := s.Roles().RolesFor(ctx, ch.ID, coCreator.ID)
	require.NoError(t, err)
	assert.Equal(t, []store.Role{store.RoleCoCreator}, coCreatorRoles)
}

// TestInviteStore_Generate_CreatorRole_Rejected proves Generate rejects
// role=creator before touching the DB (FR25/FR29: no invite path ever
// grants Founder).
func TestInviteStore_Generate_CreatorRole_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	_, err := s.Invites().Generate(ctx, ch.ID, creator.ID, store.RoleCreator)
	assert.ErrorIs(t, err, store.ErrInviteRoleUnsupported)

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM channel_invite WHERE channel_id = $1`, ch.ID).Scan(&count))
	assert.Equal(t, 0, count, "a rejected Generate call must insert no row")
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

	inv, err := s.Invites().Generate(ctx, ch.ID, creator.ID, store.RoleAnalyst)
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

// TestInviteStore_Consume_CoCreatorInvite_GrantsCoCreatorRole proves
// Consume grants the invite's OWN Role rather than a hardcoded Analyst
// (FR30), and that granted_by_person_id is the inviter (the Person who
// generated the code, FR34), not the accepter.
func TestInviteStore_Consume_CoCreatorInvite_GrantsCoCreatorRole(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	invitee, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "cc@example.com", "Co-Creator")
	require.NoError(t, err)

	inv, err := s.Invites().Generate(ctx, ch.ID, creator.ID, store.RoleCoCreator)
	require.NoError(t, err)

	require.NoError(t, s.Invites().Consume(ctx, inv.Code, invitee.ID))

	roles, err := s.Roles().RolesFor(ctx, ch.ID, invitee.ID)
	require.NoError(t, err)
	assert.Equal(t, []store.Role{store.RoleCoCreator}, roles, "Consume must grant the invite's own role (co_creator), not a hardcoded analyst")

	var grantedBy uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT granted_by_person_id FROM channel_person WHERE channel_id = $1 AND person_id = $2 AND valid_to IS NULL
	`, ch.ID, invitee.ID).Scan(&grantedBy))
	assert.Equal(t, creator.ID, grantedBy, "granted_by_person_id must be the inviter (FR34), not the accepter")
}

// TestInviteStore_Consume_ByExistingAnalyst_ClosesAnalystRowOpensCoCreatorRow
// proves the interaction with #1714's SCD2 close-and-open: an existing
// Analyst accepting a Co-Creator invite for the same Channel closes their
// analyst row and opens a co_creator row -- exactly one open row survives,
// per channel_person_channel_id_person_id_current.
func TestInviteStore_Consume_ByExistingAnalyst_ClosesAnalystRowOpensCoCreatorRow(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	person, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "p@example.com", "Person")
	require.NoError(t, err)
	require.NoError(t, s.Roles().AddRole(ctx, ch.ID, person.ID, store.RoleAnalyst, creator.ID))

	inv, err := s.Invites().Generate(ctx, ch.ID, creator.ID, store.RoleCoCreator)
	require.NoError(t, err)
	require.NoError(t, s.Invites().Consume(ctx, inv.Code, person.ID))

	roles, err := s.Roles().RolesFor(ctx, ch.ID, person.ID)
	require.NoError(t, err)
	assert.Equal(t, []store.Role{store.RoleCoCreator}, roles, "exactly one open row, now co_creator")

	var rowCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM channel_person WHERE channel_id = $1 AND person_id = $2
	`, ch.ID, person.ID).Scan(&rowCount))
	assert.Equal(t, 2, rowCount, "the prior analyst row must be closed, not deleted -- one closed plus one open")

	var closedValidTo *time.Time
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT valid_to FROM channel_person WHERE channel_id = $1 AND person_id = $2 AND role = 'analyst'
	`, ch.ID, person.ID).Scan(&closedValidTo))
	assert.NotNil(t, closedValidTo, "the previous analyst row must be closed (valid_to set)")
}

// ── RoleStore.AddRole/RemoveRole attribution (FR33/FR34) ────────────────────

// TestRoleStore_AddRole_RecordsGrantedBy proves AddRole writes the exact
// grantedByPersonID it is given onto the new row's granted_by_person_id.
func TestRoleStore_AddRole_RecordsGrantedBy(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	coCreator, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "cc@example.com", "Co-Creator")
	require.NoError(t, err)

	require.NoError(t, s.Roles().AddRole(ctx, ch.ID, coCreator.ID, store.RoleCoCreator, creator.ID))

	var role string
	var grantedBy uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT role, granted_by_person_id FROM channel_person
		WHERE channel_id = $1 AND person_id = $2 AND valid_to IS NULL
	`, ch.ID, coCreator.ID).Scan(&role, &grantedBy))
	assert.Equal(t, string(store.RoleCoCreator), role)
	assert.Equal(t, creator.ID, grantedBy, "AddRole must record the exact grantedByPersonID it was given")
}

// TestRoleStore_RemoveRole_ClosesRowWithRevokedBy proves RemoveRole closes
// the open row (valid_to set, revoked_by_person_id set to the exact
// revokedByPersonID given), leaves zero open rows for the pair afterward,
// and inserts no replacement row.
func TestRoleStore_RemoveRole_ClosesRowWithRevokedBy(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	analyst, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "a@example.com", "Analyst")
	require.NoError(t, err)
	require.NoError(t, s.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst, creator.ID))

	removed, err := s.Roles().RemoveRole(ctx, ch.ID, analyst.ID, creator.ID)
	require.NoError(t, err)
	assert.True(t, removed)

	var totalCount, openCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM channel_person WHERE channel_id = $1 AND person_id = $2
	`, ch.ID, analyst.ID).Scan(&totalCount))
	assert.Equal(t, 1, totalCount, "RemoveRole must not insert a replacement row -- exactly the one closed row must remain")

	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM channel_person WHERE channel_id = $1 AND person_id = $2 AND valid_to IS NULL
	`, ch.ID, analyst.ID).Scan(&openCount))
	assert.Equal(t, 0, openCount, "RemoveRole must leave zero open rows for the pair")

	var validTo *time.Time
	var revokedBy uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT valid_to, revoked_by_person_id FROM channel_person WHERE channel_id = $1 AND person_id = $2
	`, ch.ID, analyst.ID).Scan(&validTo, &revokedBy))
	require.NotNil(t, validTo)
	assert.Equal(t, creator.ID, revokedBy, "RemoveRole must record the exact revokedByPersonID it was given")

	roles, err := s.Roles().RolesFor(ctx, ch.ID, analyst.ID)
	require.NoError(t, err)
	assert.Empty(t, roles)
}

// TestRoleStore_RemoveRole_NoOpenRow_ReturnsFalseNoError proves FR33's
// idempotency: removing a Person who holds no open role on the Channel
// reports removed=false with no error, not an error.
func TestRoleStore_RemoveRole_NoOpenRow_ReturnsFalseNoError(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch, _ := setupChannel(t, ctx, s)

	unassociated, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "u@example.com", "Unassociated")
	require.NoError(t, err)

	removed, err := s.Roles().RemoveRole(ctx, ch.ID, unassociated.ID, uuid.New())
	require.NoError(t, err)
	assert.False(t, removed, "removing a Person with no open row must report removed=false, not error")
}

// ── AccessStore (FR26/FR30/FR31/FR33/FR35, NFR9) ───────────────────────────

// queryCounter is a pgx.QueryTracer that counts pool.Query/QueryRow/Exec
// calls -- used by the NFR9 tests below to prove a method issues exactly
// one SQL statement, per the mechanism issue #1716's Testing section asks
// for (a pgx.QueryTracer wrapped around the pool, reused across both NFR9
// tests here).
type queryCounter struct{ n atomic.Int64 }

func (q *queryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	q.n.Add(1)
	return ctx
}

func (q *queryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// tracedStore builds a second *store.Store against the same database as db
// (via its ConnString), but through a pool whose every query is counted by
// counter -- lets a test assert exactly how many SQL statements a call
// issued without needing pg_stat_statements.
func tracedStore(t *testing.T, ctx context.Context, db *dbtest.Postgres, counter *queryCounter) *store.Store {
	t.Helper()

	cfg, err := pgxpool.ParseConfig(db.ConnString)
	require.NoError(t, err)
	cfg.ConnConfig.Tracer = counter

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	return store.New(pool)
}

// TestAccessStore_ChannelsWithRoleForPerson_ReturnsTierPerChannel_ExcludesClosedRows
// proves FR26/FR28: a Person who is Founder on A, Co-Creator on B, Analyst
// on C, and has a closed (removed) role on D gets exactly A/B/C back, each
// paired with the right tier, ordered by Channel title.
func TestAccessStore_ChannelsWithRoleForPerson_ReturnsTierPerChannel_ExcludesClosedRows(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)

	person, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "p@example.com", "Person")
	require.NoError(t, err)

	// A: person is Founder -- self-granted by Channels().Create.
	chA, err := s.Channels().Create(ctx, "yt-"+uuid.NewString(), "A Channel", person.ID)
	require.NoError(t, err)

	// B: person is Co-Creator, granted by B's own Founder.
	founderB, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "fb@example.com", "Founder B")
	require.NoError(t, err)
	chB, err := s.Channels().Create(ctx, "yt-"+uuid.NewString(), "B Channel", founderB.ID)
	require.NoError(t, err)
	require.NoError(t, s.Roles().AddRole(ctx, chB.ID, person.ID, store.RoleCoCreator, founderB.ID))

	// C: person is Analyst, granted by C's own Founder.
	founderC, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "fc@example.com", "Founder C")
	require.NoError(t, err)
	chC, err := s.Channels().Create(ctx, "yt-"+uuid.NewString(), "C Channel", founderC.ID)
	require.NoError(t, err)
	require.NoError(t, s.Roles().AddRole(ctx, chC.ID, person.ID, store.RoleAnalyst, founderC.ID))

	// D: person held an Analyst role that was later removed -- the closed
	// row must not appear (FR28).
	founderD, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "fd@example.com", "Founder D")
	require.NoError(t, err)
	chD, err := s.Channels().Create(ctx, "yt-"+uuid.NewString(), "D Channel", founderD.ID)
	require.NoError(t, err)
	require.NoError(t, s.Roles().AddRole(ctx, chD.ID, person.ID, store.RoleAnalyst, founderD.ID))
	removed, err := s.Roles().RemoveRole(ctx, chD.ID, person.ID, founderD.ID)
	require.NoError(t, err)
	require.True(t, removed)

	got, err := s.Access().ChannelsWithRoleForPerson(ctx, person.ID)
	require.NoError(t, err)

	require.Len(t, got, 3, "must return exactly A/B/C -- the closed D row must be excluded")
	assert.Equal(t, chA.ID, got[0].Channel.ID)
	assert.Equal(t, store.RoleCreator, got[0].Role)
	assert.Equal(t, chB.ID, got[1].Channel.ID)
	assert.Equal(t, store.RoleCoCreator, got[1].Role)
	assert.Equal(t, chC.ID, got[2].Channel.ID)
	assert.Equal(t, store.RoleAnalyst, got[2].Role)
}

// TestAccessStore_ChannelsWithRoleForPerson_IsSingleQuery proves NFR9 for a
// Person on 5 Channels: exactly one SQL statement is issued regardless of
// how many Channels the Person holds a role on.
func TestAccessStore_ChannelsWithRoleForPerson_IsSingleQuery(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)

	person, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "p@example.com", "Person")
	require.NoError(t, err)

	for i := 0; i < 5; i++ {
		founder, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), fmt.Sprintf("f%d@example.com", i), fmt.Sprintf("Founder %d", i))
		require.NoError(t, err)
		ch, err := s.Channels().Create(ctx, "yt-"+uuid.NewString(), fmt.Sprintf("Channel %d", i), founder.ID)
		require.NoError(t, err)
		require.NoError(t, s.Roles().AddRole(ctx, ch.ID, person.ID, store.RoleAnalyst, founder.ID))
	}

	counter := &queryCounter{}
	traced := tracedStore(t, ctx, db, counter)

	before := counter.n.Load()
	got, err := traced.Access().ChannelsWithRoleForPerson(ctx, person.ID)
	require.NoError(t, err)
	require.Len(t, got, 5)

	assert.Equal(t, int64(1), counter.n.Load()-before,
		"ChannelsWithRoleForPerson must issue exactly one SQL query regardless of how many Channels the Person is on (NFR9)")
}

// TestAccessStore_Roster_OrdersFounderCoCreatorAnalyst_IncludesGranter
// proves FR30/FR31/FR33's presentation order (Founder, then Co-Creator,
// then Analysts by display name) and that a row with a NULL
// granted_by_person_id (the pre-M2 case migration 009 does not backfill)
// surfaces an empty granter rather than an error.
func TestAccessStore_Roster_OrdersFounderCoCreatorAnalyst_IncludesGranter(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	coCreator, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "cc@example.com", "Co-Creator")
	require.NoError(t, err)
	require.NoError(t, s.Roles().AddRole(ctx, ch.ID, coCreator.ID, store.RoleCoCreator, creator.ID))

	zackAnalyst, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "zack@example.com", "Zack Analyst")
	require.NoError(t, err)
	require.NoError(t, s.Roles().AddRole(ctx, ch.ID, zackAnalyst.ID, store.RoleAnalyst, creator.ID))

	amyAnalyst, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "amy@example.com", "Amy Analyst")
	require.NoError(t, err)
	// Insert directly, bypassing AddRole, to leave granted_by_person_id
	// NULL -- the pre-M2-row case migration 009 does not backfill.
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO channel_person (channel_id, person_id, role) VALUES ($1, $2, 'analyst')
	`, ch.ID, amyAnalyst.ID)
	require.NoError(t, err)

	got, err := s.Access().Roster(ctx, ch.ID)
	require.NoError(t, err)
	require.Len(t, got, 4)

	assert.Equal(t, creator.ID, got[0].PersonID)
	assert.Equal(t, store.RoleCreator, got[0].Role)
	assert.Equal(t, "Creator", got[0].GrantedByDisplayName, "Create() self-grants the Founder role (FR25)")

	assert.Equal(t, coCreator.ID, got[1].PersonID)
	assert.Equal(t, store.RoleCoCreator, got[1].Role)
	assert.Equal(t, "Creator", got[1].GrantedByDisplayName)

	assert.Equal(t, amyAnalyst.ID, got[2].PersonID, "Analysts ordered by display name: Amy before Zack")
	assert.Equal(t, store.RoleAnalyst, got[2].Role)
	assert.Equal(t, "", got[2].GrantedByDisplayName, "a NULL granted_by_person_id (pre-M2 row) must surface as empty, not error")

	assert.Equal(t, zackAnalyst.ID, got[3].PersonID)
	assert.Equal(t, store.RoleAnalyst, got[3].Role)
	assert.Equal(t, "Creator", got[3].GrantedByDisplayName)
}

// TestAccessStore_AuditTrail_MostRecentFirst_GrantAndRevokeEvents proves
// FR35: a grant, a promote (close-and-open), and a remove on one Channel
// yield the expected most-recent-first event sequence with correct actors
// and tiers, and that another Channel's events never leak in.
func TestAccessStore_AuditTrail_MostRecentFirst_GrantAndRevokeEvents(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch, creator := setupChannel(t, ctx, s)
	otherCh, otherCreator := setupChannel(t, ctx, s)

	subject, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "subject@example.com", "Subject")
	require.NoError(t, err)
	otherSubject, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "other@example.com", "Other Subject")
	require.NoError(t, err)

	require.NoError(t, s.Roles().AddRole(ctx, ch.ID, subject.ID, store.RoleAnalyst, creator.ID))
	// Promote: SCD2 close-and-open in one transaction -- closes the
	// analyst row (a 'revoked' event) and opens a co_creator row (a
	// 'granted' event) at the same occurred_at.
	require.NoError(t, s.Roles().AddRole(ctx, ch.ID, subject.ID, store.RoleCoCreator, creator.ID))
	removed, err := s.Roles().RemoveRole(ctx, ch.ID, subject.ID, creator.ID)
	require.NoError(t, err)
	require.True(t, removed)

	// Another Channel's events must never leak in.
	require.NoError(t, s.Roles().AddRole(ctx, otherCh.ID, otherSubject.ID, store.RoleAnalyst, otherCreator.ID))

	events, err := s.Access().AuditTrail(ctx, ch.ID, 0)
	require.NoError(t, err)
	require.Len(t, events, 5, "Founder self-grant, plus grant/promote(revoke+grant)/remove on subject == 5 events")

	assert.Equal(t, "revoked", events[0].Event)
	assert.Equal(t, store.RoleCoCreator, events[0].Role)
	assert.Equal(t, subject.ID, events[0].SubjectPersonID)
	require.NotNil(t, events[0].ActorPersonID)
	assert.Equal(t, creator.ID, *events[0].ActorPersonID)

	assert.Equal(t, "granted", events[1].Event)
	assert.Equal(t, store.RoleCoCreator, events[1].Role)
	assert.Equal(t, subject.ID, events[1].SubjectPersonID)
	require.NotNil(t, events[1].ActorPersonID)
	assert.Equal(t, creator.ID, *events[1].ActorPersonID)

	assert.Equal(t, "revoked", events[2].Event)
	assert.Equal(t, store.RoleAnalyst, events[2].Role)
	assert.Equal(t, subject.ID, events[2].SubjectPersonID)

	assert.Equal(t, "granted", events[3].Event)
	assert.Equal(t, store.RoleAnalyst, events[3].Role)
	assert.Equal(t, subject.ID, events[3].SubjectPersonID)
	require.NotNil(t, events[3].ActorPersonID)
	assert.Equal(t, creator.ID, *events[3].ActorPersonID)

	assert.Equal(t, "granted", events[4].Event)
	assert.Equal(t, store.RoleCreator, events[4].Role)
	assert.Equal(t, creator.ID, events[4].SubjectPersonID, "the Founder's own self-grant at Channel-connect is the oldest event")

	for _, ev := range events {
		assert.NotEqual(t, otherSubject.ID, ev.SubjectPersonID, "another Channel's events must never leak into this Channel's trail")
	}

	otherEvents, err := s.Access().AuditTrail(ctx, otherCh.ID, 0)
	require.NoError(t, err)
	for _, ev := range otherEvents {
		assert.NotEqual(t, subject.ID, ev.SubjectPersonID, "this Channel's events must never leak into another Channel's trail")
	}
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

	videos, _, err := s.Sync().ListSchedule(ctx, ch.ID, nil, nil, true, 0)
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
	synced, _, err := s.Sync().ListSchedule(ctx, ch.ID, nil, nil, true, 0)
	require.NoError(t, err)
	require.Len(t, synced, 1)
	video = synced[0]

	require.NoError(t, s.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, ScheduleEntryID: &entry.ID, Confidence: 0.5, State: store.MatchStatePending,
	}))
	pending, _, err := s.Matches().ListPending(ctx, ch.ID, nil, 0)
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
	synced, _, err := s.Sync().ListSchedule(ctx, ch.ID, nil, nil, true, 0)
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
// (mcp_credential, #1575 -- 004 is Channel-connect's channel_credential
// store, #1571), again for 006 (mcp_credential dropped and recreated
// against libs/go/mcpauth's schema contract, #1643), again for 007
// (mcp_oauth_client + mcp_auth_code, mcpauth's OAuth2 authorization-code +
// PKCE front end, #1646), again for 008 (strategy/strategy_verdict,
// #1637), again for 009 (co_creator_tier, #1713), and again for 010
// (video_script + video_schedule_match.video_script_id, issues
// #1823/#1824), so the version assertion and table list below cover all
// of them rather than any single one.
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
	assert.Equal(t, uint(10), version, "highest migration in schema.Migrations is 010_video_script")

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
		"strategy", "strategy_verdict",
		"video_script",
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

	// Migration 010's video_schedule_match.video_script_id re-anchor column
	// (FR45) must also survive the down/up cycle -- a partial down that
	// forgot to drop it (or a partial up that forgot to re-add it) would
	// surface here.
	var hasVideoScriptID bool
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'video_schedule_match' AND column_name = 'video_script_id')`,
	).Scan(&hasVideoScriptID))
	assert.True(t, hasVideoScriptID, "video_schedule_match.video_script_id must exist after up/down/up")

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

// ── Migration 009 (co_creator tier, FR29/FR33/FR34/FR35, NFR6/NFR10/NFR11) ──

// TestMigration009_CoCreatorRoleAccepted_UnknownRoleRejected proves NFR6's
// widened channel_person_role_check accepts the new 'co_creator' value but
// still rejects anything outside the three-tier list -- the CHECK is a
// closed enumeration, not merely "anything goes now".
func TestMigration009_CoCreatorRoleAccepted_UnknownRoleRejected(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, _ := setupChannel(t, ctx, s)

	coCreator, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "cc@example.com", "Co-Creator")
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO channel_person (channel_id, person_id, role) VALUES ($1, $2, 'co_creator')
	`, ch.ID, coCreator.ID)
	require.NoError(t, err, "role='co_creator' must satisfy the widened channel_person_role_check (NFR6)")

	unknown, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "u@example.com", "Unknown")
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO channel_person (channel_id, person_id, role) VALUES ($1, $2, 'owner')
	`, ch.ID, unknown.ID)
	assert.Error(t, err, "role='owner' is not in the three-tier list and must still violate the CHECK")
}

// TestMigration009_SecondLiveFounderRejected proves NFR10's
// channel_person_channel_id_founder_current partial unique index enforces
// "at most one open creator row per Channel", while leaving non-creator
// roles and non-open creator rows unaffected.
func TestMigration009_SecondLiveFounderRejected(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	// setupChannel's Create already leaves one open role='creator' row for
	// `creator` on ch -- the row every insert below is tested against.
	ch, _ := setupChannel(t, ctx, s)

	secondFounder, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "f2@example.com", "Second Founder")
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO channel_person (channel_id, person_id, role) VALUES ($1, $2, 'creator')
	`, ch.ID, secondFounder.ID)
	assert.Error(t, err, "a second open role='creator' row on the same channel must violate "+
		"channel_person_channel_id_founder_current (NFR10)")

	coCreator, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "cc@example.com", "Co-Creator")
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO channel_person (channel_id, person_id, role) VALUES ($1, $2, 'co_creator')
	`, ch.ID, coCreator.ID)
	assert.NoError(t, err, "a second open co_creator row on the same channel is unaffected by the "+
		"Founder-only backstop")

	closedFounder, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "f3@example.com", "Closed Founder")
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO channel_person (channel_id, person_id, role, valid_to)
		VALUES ($1, $2, 'creator', NOW())
	`, ch.ID, closedFounder.ID)
	assert.NoError(t, err, "a CLOSED creator row (valid_to set) alongside the channel's already-open "+
		"creator row must be allowed -- the index is scoped WHERE valid_to IS NULL")
}

// TestMigration009_LiveAnalystAndCoCreatorInvitesCoexist proves NFR11's
// rescoped channel_invite_channel_id_role_live index allows one live invite
// per (channel, role) rather than one live invite per channel -- a live
// Analyst invite and a live Co-Creator invite must coexist (FR30), but two
// live invites of the same tier must not.
func TestMigration009_LiveAnalystAndCoCreatorInvitesCoexist(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	_, err := db.Pool.Exec(ctx, `
		INSERT INTO channel_invite (channel_id, code, created_by_person_id, role)
		VALUES ($1, $2, $3, 'analyst')
	`, ch.ID, "code-analyst-"+uuid.NewString(), creator.ID)
	require.NoError(t, err)

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO channel_invite (channel_id, code, created_by_person_id, role)
		VALUES ($1, $2, $3, 'co_creator')
	`, ch.ID, "code-cocreator-"+uuid.NewString(), creator.ID)
	assert.NoError(t, err, "a live analyst invite and a live co_creator invite on the same channel "+
		"must coexist (FR30)")

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO channel_invite (channel_id, code, created_by_person_id, role)
		VALUES ($1, $2, $3, 'analyst')
	`, ch.ID, "code-analyst-2-"+uuid.NewString(), creator.ID)
	assert.Error(t, err, "a second live analyst invite on the same channel must violate "+
		"channel_invite_channel_id_role_live")
}

// TestMigration009_AuditViewEmitsGrantAndRevokeEvents proves FR35's
// v_channel_person_audit emits exactly one 'granted' event per
// channel_person row and, only once the row is closed, exactly one
// additional 'revoked' event -- with the right actor identity on each side.
func TestMigration009_AuditViewEmitsGrantAndRevokeEvents(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	// setupChannel's Create leaves an open role='creator' row for `creator`
	// with `creator` recorded as their own granted_by_person_id (FR25's
	// self-grant at connect, #1714) -- this is the "still-open, self-
	// granted" case below.
	ch, creator := setupChannel(t, ctx, s)

	granter, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "granter@example.com", "Granter")
	require.NoError(t, err)
	revoker, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "revoker@example.com", "Revoker")
	require.NoError(t, err)
	subject, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "subject@example.com", "Subject")
	require.NoError(t, err)

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO channel_person (channel_id, person_id, role, granted_by_person_id)
		VALUES ($1, $2, 'co_creator', $3)
	`, ch.ID, subject.ID, granter.ID)
	require.NoError(t, err)

	_, err = db.Pool.Exec(ctx, `
		UPDATE channel_person SET valid_to = NOW(), revoked_by_person_id = $3
		WHERE channel_id = $1 AND person_id = $2 AND valid_to IS NULL
	`, ch.ID, subject.ID, revoker.ID)
	require.NoError(t, err)

	type auditRow struct {
		event            string
		occurredAt       time.Time
		role             string
		actorPersonID    *uuid.UUID
		actorDisplayName *string
	}
	queryRows := func(personID uuid.UUID) []auditRow {
		rows, err := db.Pool.Query(ctx, `
			SELECT event, occurred_at, role, actor_person_id, actor_display_name
			FROM v_channel_person_audit
			WHERE channel_id = $1 AND subject_person_id = $2
			ORDER BY occurred_at
		`, ch.ID, personID)
		require.NoError(t, err)
		defer rows.Close()
		var out []auditRow
		for rows.Next() {
			var r auditRow
			require.NoError(t, rows.Scan(&r.event, &r.occurredAt, &r.role, &r.actorPersonID, &r.actorDisplayName))
			out = append(out, r)
		}
		require.NoError(t, rows.Err())
		return out
	}

	subjectRows := queryRows(subject.ID)
	require.Len(t, subjectRows, 2, "a granted-then-revoked row must yield exactly two audit events")
	assert.Equal(t, "granted", subjectRows[0].event)
	assert.Equal(t, "co_creator", subjectRows[0].role)
	require.NotNil(t, subjectRows[0].actorPersonID)
	assert.Equal(t, granter.ID, *subjectRows[0].actorPersonID)
	require.NotNil(t, subjectRows[0].actorDisplayName)
	assert.Equal(t, "Granter", *subjectRows[0].actorDisplayName)

	assert.Equal(t, "revoked", subjectRows[1].event)
	assert.Equal(t, "co_creator", subjectRows[1].role)
	require.NotNil(t, subjectRows[1].actorPersonID)
	assert.Equal(t, revoker.ID, *subjectRows[1].actorPersonID)
	require.NotNil(t, subjectRows[1].actorDisplayName)
	assert.Equal(t, "Revoker", *subjectRows[1].actorDisplayName)
	assert.False(t, subjectRows[1].occurredAt.Before(subjectRows[0].occurredAt),
		"the revoked event's occurred_at (valid_to) must not precede the granted event's (valid_from)")

	creatorRows := queryRows(creator.ID)
	require.Len(t, creatorRows, 1, "a still-open row must yield exactly one granted event and no revoked event")
	assert.Equal(t, "granted", creatorRows[0].event)
	assert.Equal(t, "creator", creatorRows[0].role)
	require.NotNil(t, creatorRows[0].actorPersonID, "Create() self-grants the Founder role (FR25) -- actor_person_id must be the creator themself")
	assert.Equal(t, creator.ID, *creatorRows[0].actorPersonID)
	require.NotNil(t, creatorRows[0].actorDisplayName)
	assert.Equal(t, "Creator", *creatorRows[0].actorDisplayName)
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

// ── MyWorkStore (issue #1717, FR27/FR28, NFR9) ─────────────────────────────

// setupMyWorkOutcomeChain builds one viable Idea on ch with a committed
// schedule entry, a published SyncedVideo, one VideoMetrics snapshot, and
// an "auto" video_schedule_match linking them -- the qualifying-row shape
// BrowseStore.PredictionVsOutcome (and MyWorkStore's outcome section,
// which shares its join) requires. Mirrors mcp/tools/
// browse_integration_test.go's newFullChainFixture, at the store layer.
func setupMyWorkOutcomeChain(t *testing.T, ctx context.Context, s *store.Store, ch store.Channel, creator store.Person, ideaTitle string) (store.Idea, store.Verdict) {
	t.Helper()

	idea, err := s.Ideas().Create(ctx, ch.ID, ideaTitle, creator.ID)
	require.NoError(t, err)

	v, err := s.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: ideaTitle + " reasoning", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	entry, err := s.Schedules().SaveDraft(ctx, store.SaveDraftInput{
		ChannelID: ch.ID, IdeaID: idea.ID, VerdictID: v.ID,
		ProposedPublishAt: time.Now().Add(24 * time.Hour), CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)
	require.NoError(t, s.Schedules().Approve(ctx, entry.ID, creator.ID))

	videoTitle := ideaTitle + " Video"
	publishedAt := time.Now().Add(-time.Hour)
	require.NoError(t, s.Sync().UpsertVideos(ctx, ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: "yt-" + uuid.NewString(), Title: videoTitle,
		PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: &publishedAt, LastSyncedAt: time.Now(),
	}}))
	synced, _, err := s.Sync().ListSchedule(ctx, ch.ID, nil, nil, true, 0)
	require.NoError(t, err)
	var video store.SyncedVideo
	for _, sv := range synced {
		if sv.Title == videoTitle {
			video = sv
		}
	}
	require.NotEqual(t, uuid.Nil, video.ID, "the just-synced video must be found by title")

	require.NoError(t, s.Sync().UpsertMetrics(ctx, []store.VideoMetrics{{
		SyncedVideoID: video.ID, Views: ptrInt64(500), MeasuredAt: time.Now(),
	}}))

	require.NoError(t, s.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, ScheduleEntryID: &entry.ID, Confidence: 0.9, State: store.MatchStateAuto,
	}))

	return idea, v
}

// TestMyWorkStore_SummariesForPerson_CoversEveryAssociatedChannel_WithPerSectionContent
// proves FR27: a Person on three Channels at three different tiers, each
// with distinct note/verdict/schedule/outcome fixtures, gets exactly those
// three Channels back, each paired with its own tier, with no cross-Channel
// bleed into any section.
func TestMyWorkStore_SummariesForPerson_CoversEveryAssociatedChannel_WithPerSectionContent(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)

	person, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "p@example.com", "Person")
	require.NoError(t, err)

	// A: person is Founder -- one draft-only schedule_entry, no outcome yet.
	chA, err := s.Channels().Create(ctx, "yt-"+uuid.NewString(), "A Channel", person.ID)
	require.NoError(t, err)
	noteA, err := s.Research().SaveNote(ctx, store.SaveNoteInput{ChannelID: chA.ID, Text: "A note", AuthorPersonID: person.ID})
	require.NoError(t, err)
	ideaA, err := s.Ideas().Create(ctx, chA.ID, "A Idea", person.ID)
	require.NoError(t, err)
	verdictA, err := s.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: ideaA.ID, Verdict: store.VerdictViable, Reasoning: "A reasoning", AuthorPersonID: person.ID,
	})
	require.NoError(t, err)
	_, err = s.Schedules().SaveDraft(ctx, store.SaveDraftInput{
		ChannelID: chA.ID, IdeaID: ideaA.ID, VerdictID: verdictA.ID,
		ProposedPublishAt: time.Now().Add(48 * time.Hour), CreatedByPersonID: person.ID,
	})
	require.NoError(t, err)

	// B: person is Co-Creator, granted by B's own Founder -- full outcome
	// chain (committed schedule entry, published matched video).
	founderB, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "fb@example.com", "Founder B")
	require.NoError(t, err)
	chB, err := s.Channels().Create(ctx, "yt-"+uuid.NewString(), "B Channel", founderB.ID)
	require.NoError(t, err)
	require.NoError(t, s.Roles().AddRole(ctx, chB.ID, person.ID, store.RoleCoCreator, founderB.ID))
	noteB, err := s.Research().SaveNote(ctx, store.SaveNoteInput{ChannelID: chB.ID, Text: "B note", AuthorPersonID: founderB.ID})
	require.NoError(t, err)
	ideaB, verdictB := setupMyWorkOutcomeChain(t, ctx, s, chB, founderB, "B Idea")

	// C: person is Analyst, granted by C's own Founder -- a note and a
	// (not-viable) verdict only, no schedule/outcome data at all.
	founderC, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "fc@example.com", "Founder C")
	require.NoError(t, err)
	chC, err := s.Channels().Create(ctx, "yt-"+uuid.NewString(), "C Channel", founderC.ID)
	require.NoError(t, err)
	require.NoError(t, s.Roles().AddRole(ctx, chC.ID, person.ID, store.RoleAnalyst, founderC.ID))
	noteC, err := s.Research().SaveNote(ctx, store.SaveNoteInput{ChannelID: chC.ID, Text: "C note", AuthorPersonID: founderC.ID})
	require.NoError(t, err)
	ideaC, err := s.Ideas().Create(ctx, chC.ID, "C Idea", founderC.ID)
	require.NoError(t, err)
	verdictC, err := s.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: ideaC.ID, Verdict: store.VerdictNotViable, Reasoning: "C reasoning", AuthorPersonID: founderC.ID,
	})
	require.NoError(t, err)

	got, err := s.MyWork().SummariesForPerson(ctx, person.ID, 5)
	require.NoError(t, err)
	require.Len(t, got, 3, "must cover every Channel the Person holds an open role on")

	// A: Founder tier, one draft, no outcome.
	assert.Equal(t, chA.ID, got[0].Channel.ID)
	assert.Equal(t, store.RoleCreator, got[0].Role)
	require.Len(t, got[0].LatestNotes, 1)
	assert.Equal(t, noteA.ID, got[0].LatestNotes[0].ID)
	require.NotNil(t, got[0].LatestVerdict)
	assert.Equal(t, verdictA.ID, got[0].LatestVerdict.VerdictID)
	assert.Equal(t, ideaA.ID, got[0].LatestVerdict.IdeaID)
	assert.Equal(t, 1, got[0].ScheduleState.DraftCount)
	assert.Equal(t, 0, got[0].ScheduleState.CommittedCount)
	require.NotNil(t, got[0].ScheduleState.NextProposedPublishAt)
	assert.Nil(t, got[0].LatestOutcome, "A has no published/matched video yet")

	// B: Co-Creator tier, full outcome chain.
	assert.Equal(t, chB.ID, got[1].Channel.ID)
	assert.Equal(t, store.RoleCoCreator, got[1].Role)
	require.Len(t, got[1].LatestNotes, 1)
	assert.Equal(t, noteB.ID, got[1].LatestNotes[0].ID)
	require.NotNil(t, got[1].LatestVerdict)
	assert.Equal(t, verdictB.ID, got[1].LatestVerdict.VerdictID)
	assert.Equal(t, 0, got[1].ScheduleState.DraftCount)
	assert.Equal(t, 1, got[1].ScheduleState.CommittedCount)
	require.NotNil(t, got[1].LatestOutcome, "B has a full committed/matched/published chain")
	assert.Equal(t, ideaB.ID, got[1].LatestOutcome.IdeaID)
	assert.Equal(t, verdictB.ID, got[1].LatestOutcome.VerdictID)

	// C: Analyst tier, note + verdict only.
	assert.Equal(t, chC.ID, got[2].Channel.ID)
	assert.Equal(t, store.RoleAnalyst, got[2].Role)
	require.Len(t, got[2].LatestNotes, 1)
	assert.Equal(t, noteC.ID, got[2].LatestNotes[0].ID)
	require.NotNil(t, got[2].LatestVerdict)
	assert.Equal(t, verdictC.ID, got[2].LatestVerdict.VerdictID)
	assert.Equal(t, 0, got[2].ScheduleState.DraftCount)
	assert.Equal(t, 0, got[2].ScheduleState.CommittedCount)
	assert.Nil(t, got[2].ScheduleState.NextProposedPublishAt)
	assert.Nil(t, got[2].LatestOutcome)
}

// TestMyWorkStore_SummariesForPerson_EmptyChannelStillAppears proves a
// freshly connected Channel with no notes/verdicts/schedule/outcome data
// still yields an entry -- empty sections, never a missing Channel.
func TestMyWorkStore_SummariesForPerson_EmptyChannelStillAppears(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	got, err := s.MyWork().SummariesForPerson(ctx, creator.ID, 5)
	require.NoError(t, err)
	require.Len(t, got, 1)

	assert.Equal(t, ch.ID, got[0].Channel.ID)
	assert.Equal(t, store.RoleCreator, got[0].Role)
	assert.NotNil(t, got[0].LatestNotes, "must be an empty slice, not nil, for a Channel with no notes")
	assert.Empty(t, got[0].LatestNotes)
	assert.Nil(t, got[0].LatestVerdict)
	assert.Equal(t, store.ScheduleDraftState{}, got[0].ScheduleState)
	assert.Nil(t, got[0].LatestOutcome)
}

// TestMyWorkStore_SummariesForPerson_QueryCountIsBoundedAcrossChannelCount
// proves NFR9 -- the whole point of this task: the number of SQL
// statements SummariesForPerson issues for a Person on 2 Channels equals
// the number for a Person on 10 Channels, and both are <= 5 (one Channel
// listing plus one each for notes/verdicts/schedule/outcomes). A naive
// per-Channel loop would fail this by construction (the count would scale
// with Channel count) -- this is the test that must be written and watched
// fail against such a loop before this task's implementation exists.
func TestMyWorkStore_SummariesForPerson_QueryCountIsBoundedAcrossChannelCount(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)

	countQueriesFor := func(numChannels int) int64 {
		person, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), fmt.Sprintf("p%d@example.com", numChannels), "Person")
		require.NoError(t, err)

		for i := 0; i < numChannels; i++ {
			founder, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), fmt.Sprintf("f%d-%d@example.com", numChannels, i), fmt.Sprintf("Founder %d-%d", numChannels, i))
			require.NoError(t, err)
			ch, err := s.Channels().Create(ctx, "yt-"+uuid.NewString(), fmt.Sprintf("Channel %d-%d", numChannels, i), founder.ID)
			require.NoError(t, err)
			require.NoError(t, s.Roles().AddRole(ctx, ch.ID, person.ID, store.RoleAnalyst, founder.ID))
			_, err = s.Research().SaveNote(ctx, store.SaveNoteInput{ChannelID: ch.ID, Text: "note", AuthorPersonID: founder.ID})
			require.NoError(t, err)
		}

		counter := &queryCounter{}
		traced := tracedStore(t, ctx, db, counter)

		before := counter.n.Load()
		got, err := traced.MyWork().SummariesForPerson(ctx, person.ID, 5)
		require.NoError(t, err)
		require.Len(t, got, numChannels)

		return counter.n.Load() - before
	}

	count2 := countQueriesFor(2)
	count10 := countQueriesFor(10)

	assert.Equal(t, count2, count10, "query count must not scale with Channel count (NFR9)")
	assert.LessOrEqual(t, count2, int64(5), "at most 5 statements total: channel list, notes, verdicts, schedule, outcomes")
}

// TestMyWorkStore_SummariesForPerson_RevokedRoleDropsChannelImmediately
// proves FR28: revoking a Person's role on a Channel drops it from the
// very next SummariesForPerson call, with no re-auth and no new store/
// connection required.
func TestMyWorkStore_SummariesForPerson_RevokedRoleDropsChannelImmediately(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)

	person, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "p@example.com", "Person")
	require.NoError(t, err)
	founder, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "f@example.com", "Founder")
	require.NoError(t, err)
	ch, err := s.Channels().Create(ctx, "yt-"+uuid.NewString(), "Channel", founder.ID)
	require.NoError(t, err)
	require.NoError(t, s.Roles().AddRole(ctx, ch.ID, person.ID, store.RoleAnalyst, founder.ID))

	before, err := s.MyWork().SummariesForPerson(ctx, person.ID, 5)
	require.NoError(t, err)
	require.Len(t, before, 1, "must appear while the role is open")

	removed, err := s.Roles().RemoveRole(ctx, ch.ID, person.ID, founder.ID)
	require.NoError(t, err)
	require.True(t, removed)

	after, err := s.MyWork().SummariesForPerson(ctx, person.ID, 5)
	require.NoError(t, err)
	assert.Empty(t, after, "a revoked role must drop the Channel out on the very next call -- no re-auth, no new connection, no cache")
}

// TestMyWorkStore_SummariesForPerson_NoRoles_ReturnsEmpty_NoExtraQueries
// proves a Person with no open role on any Channel gets an empty slice
// back after exactly the one Channel-listing query, with no further
// section queries issued.
func TestMyWorkStore_SummariesForPerson_NoRoles_ReturnsEmpty_NoExtraQueries(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)

	unassociated, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "u@example.com", "Unassociated")
	require.NoError(t, err)

	counter := &queryCounter{}
	traced := tracedStore(t, ctx, db, counter)

	before := counter.n.Load()
	got, err := traced.MyWork().SummariesForPerson(ctx, unassociated.ID, 5)
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Equal(t, int64(1), counter.n.Load()-before, "no open role anywhere must short-circuit after the one Channel-listing query")
}
