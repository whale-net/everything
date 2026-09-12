//go:build integration

// Real-Postgres coverage for PersonIdentityStore.FindOrCreateByIssSub
// (migration 020, issue #2116, FR12(b)) -- the (iss, sub) -> Person
// auto-provisioning find-or-create this package's whagent-net
// authentication path resolves every verified whagent Claim against -- and
// for PersonIdentityStore.LinkToExistingPerson (issue #2599, FR6-FR9,
// NFR4), the link-to-existing-Person write path an already-signed-in
// Operator's confirmed link uses INSTEAD of FindOrCreateByIssSub's
// auto-provisioning branch. See store_integration_test.go's package doc
// for why this file only builds under the "integration" build tag.
package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/libs/go/dbtest"
)

// ── PersonIdentityStore (FR12(b)) ───────────────────────────────────────────

func TestPersonIdentityStore_FindOrCreateByIssSub_UnseenPairCreatesExactlyOnePerson(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)

	p, created, err := s.PersonIdentities().FindOrCreateByIssSub(ctx, "https://keycloak.example.test/realms/humans", "human-1")
	require.NoError(t, err)
	assert.True(t, created, "an unseen (iss, sub) pair must auto-provision a new Person")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM person WHERE id = $1`, p.ID).Scan(&count))
	assert.Equal(t, 1, count, "exactly one Person row must exist for the newly provisioned pair")

	// Identity-only provisioning (FR10/FR12(b)): no email, no display
	// name, and no google_subject -- a whagent-net auto-provisioned
	// Person may never sign into `web`.
	assert.Empty(t, p.Email)
	assert.Empty(t, p.DisplayName)
	assert.Empty(t, p.GoogleSubject)
}

func TestPersonIdentityStore_FindOrCreateByIssSub_SamePairResolvesToSamePersonNoDuplicate(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	const iss, sub = "https://keycloak.example.test/realms/humans", "human-1"

	first, created1, err := s.PersonIdentities().FindOrCreateByIssSub(ctx, iss, sub)
	require.NoError(t, err)
	assert.True(t, created1)

	second, created2, err := s.PersonIdentities().FindOrCreateByIssSub(ctx, iss, sub)
	require.NoError(t, err)
	assert.False(t, created2, "a second call for the same (iss, sub) pair must not create a new Person")
	assert.Equal(t, first.ID, second.ID)

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM person_oidc_identity WHERE iss = $1 AND sub = $2`, iss, sub).Scan(&count))
	assert.Equal(t, 1, count, "the pair must link to exactly one person_oidc_identity row")
}

func TestPersonIdentityStore_FindOrCreateByIssSub_DifferentSubsSameIssuer_TwoDistinctPersons(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	const iss = "https://keycloak.example.test/realms/humans"

	p1, _, err := s.PersonIdentities().FindOrCreateByIssSub(ctx, iss, "human-1")
	require.NoError(t, err)
	p2, _, err := s.PersonIdentities().FindOrCreateByIssSub(ctx, iss, "human-2")
	require.NoError(t, err)

	assert.NotEqual(t, p1.ID, p2.ID, "two different sub values under the same iss must resolve to two distinct persons")
}

// TestPersonIdentityStore_FindOrCreateByIssSub_SameSubDifferentIssuers_TwoDistinctPersons
// is the test that proves this store is keyed on the PAIR (iss, sub), not
// on sub alone -- migration 020's whole reason for existing rather than
// reusing person.google_subject. Red/green per this task's own Testing
// note: temporarily keying the lookup on sub alone makes this go red.
func TestPersonIdentityStore_FindOrCreateByIssSub_SameSubDifferentIssuers_TwoDistinctPersons(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	const sub = "human-1"

	p1, _, err := s.PersonIdentities().FindOrCreateByIssSub(ctx, "https://keycloak-a.example.test/realms/humans", sub)
	require.NoError(t, err)
	p2, _, err := s.PersonIdentities().FindOrCreateByIssSub(ctx, "https://keycloak-b.example.test/realms/humans", sub)
	require.NoError(t, err)

	assert.NotEqual(t, p1.ID, p2.ID, "the same sub under two different iss values must resolve to two distinct persons")
}

// TestPersonIdentityStore_FindOrCreateByIssSub_ConcurrentFirstSight_CreatesExactlyOnePerson
// drives many concurrent first-sight calls for the exact same (iss, sub)
// pair -- the race FindOrCreateByIssSub's doc comment describes Postgres's
// own unique-index locking (person_oidc_identity_iss_sub) as resolving:
// exactly one of them must win and create the Person, every other caller
// must resolve to that same winner, and no orphaned, identity-less Person
// row may be left behind by a loser's rolled-back transaction.
func TestPersonIdentityStore_FindOrCreateByIssSub_ConcurrentFirstSight_CreatesExactlyOnePerson(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	const (
		iss           = "https://keycloak.example.test/realms/humans"
		sub           = "human-concurrent"
		numGoroutines = 10
	)

	var wg sync.WaitGroup
	personIDs := make([]uuid.UUID, numGoroutines)
	errs := make([]error, numGoroutines)
	for i := range numGoroutines {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, _, err := s.PersonIdentities().FindOrCreateByIssSub(ctx, iss, sub)
			personIDs[i] = p.ID
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "goroutine %d", i)
	}
	first := personIDs[0]
	for i, id := range personIDs {
		assert.Equal(t, first, id, "goroutine %d resolved to a different person_id than goroutine 0", i)
	}

	var personCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM person p
		JOIN person_oidc_identity i ON i.person_id = p.id
		WHERE i.iss = $1 AND i.sub = $2
	`, iss, sub).Scan(&personCount))
	assert.Equal(t, 1, personCount, "concurrent first-sight calls for the same pair must create exactly one linked Person row")

	// No orphaned (identity-less) Person rows from a losing goroutine's
	// rolled-back transaction: total person_oidc_identity-less rows this
	// test itself created should be zero -- every person row created by
	// FindOrCreateByIssSub is always immediately linked in the same
	// transaction, or rolled back entirely.
	var totalPersonRows int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM person`).Scan(&totalPersonRows))
	assert.Equal(t, 1, totalPersonRows, "exactly one Person row total must exist after the concurrent race -- no orphans from a rolled-back loser")
}

// ── PersonIdentityStore.LinkToExistingPerson (issue #2599, FR6-FR9, NFR4) ──

// newExistingPerson provisions a Person the way `web`'s Google session path
// (person.go's UpsertByGoogleSubject) would -- an already-signed-in
// Operator -- for LinkToExistingPerson's tests to link an (iss, sub) pair
// onto. LinkToExistingPerson itself must never create a Person (FR6's
// "no new person row" assertion depends on every test using an existing,
// separately-provisioned one).
func newExistingPerson(t *testing.T, s *store.Store, googleSubject string) uuid.UUID {
	t.Helper()
	p, created, err := s.Persons().UpsertByGoogleSubject(context.Background(), googleSubject, googleSubject+"@example.test", "Operator "+googleSubject)
	require.NoError(t, err)
	require.True(t, created)
	return p.ID
}

func personCount(t *testing.T, db *dbtest.Postgres) int {
	t.Helper()
	var count int
	require.NoError(t, db.Pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM person`).Scan(&count))
	return count
}

func identityRow(t *testing.T, db *dbtest.Postgres, iss, sub string) (personID uuid.UUID, found bool) {
	t.Helper()
	err := db.Pool.QueryRow(context.Background(), `
		SELECT person_id FROM person_oidc_identity WHERE iss = $1 AND sub = $2
	`, iss, sub).Scan(&personID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false
	}
	require.NoError(t, err)
	return personID, true
}

func identityRowCount(t *testing.T, db *dbtest.Postgres, iss, sub string) int {
	t.Helper()
	var count int
	require.NoError(t, db.Pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM person_oidc_identity WHERE iss = $1 AND sub = $2
	`, iss, sub).Scan(&count))
	return count
}

// TestPersonIdentityStore_LinkToExistingPerson_UnseenPair_CreatesLink covers
// FR6: linking an unseen (iss, sub) pair to an existing Person writes
// exactly one person_oidc_identity row and creates no new Person row.
func TestPersonIdentityStore_LinkToExistingPerson_UnseenPair_CreatesLink(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	const iss, sub = "https://accounts.google.test", "human-link-1"

	personID := newExistingPerson(t, s, "google-sub-link-1")
	before := personCount(t, db)

	outcome, err := s.PersonIdentities().LinkToExistingPerson(ctx, iss, sub, personID)
	require.NoError(t, err)
	assert.Equal(t, store.LinkCreated, outcome)

	assert.Equal(t, 1, identityRowCount(t, db, iss, sub), "exactly one link row must exist for the pair")
	linkedID, found := identityRow(t, db, iss, sub)
	require.True(t, found)
	assert.Equal(t, personID, linkedID)

	assert.Equal(t, before, personCount(t, db), "LinkToExistingPerson must never create a Person row")
}

// TestPersonIdentityStore_LinkToExistingPerson_SamePersonAgain_IsNoOp covers
// FR9: relinking the same (iss, sub) pair to the SAME Person it already
// points at is a genuine no-op -- no error, no duplicate row.
func TestPersonIdentityStore_LinkToExistingPerson_SamePersonAgain_IsNoOp(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	const iss, sub = "https://accounts.google.test", "human-link-2"

	personID := newExistingPerson(t, s, "google-sub-link-2")

	outcome1, err := s.PersonIdentities().LinkToExistingPerson(ctx, iss, sub, personID)
	require.NoError(t, err)
	require.Equal(t, store.LinkCreated, outcome1)

	outcome2, err := s.PersonIdentities().LinkToExistingPerson(ctx, iss, sub, personID)
	require.NoError(t, err, "relinking the same pair to the same person must not error")
	assert.Equal(t, store.LinkAlreadyOwned, outcome2)

	assert.Equal(t, 1, identityRowCount(t, db, iss, sub), "the no-op relink must not create a duplicate row")
}

// TestPersonIdentityStore_LinkToExistingPerson_DifferentPerson_Conflicts
// covers FR8: an (iss, sub) pair already linked to Person A must refuse a
// link to Person B outright -- no merge, no reassignment, no overwrite.
func TestPersonIdentityStore_LinkToExistingPerson_DifferentPerson_Conflicts(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	const iss, sub = "https://accounts.google.test", "human-link-3"

	personA := newExistingPerson(t, s, "google-sub-link-3a")
	personB := newExistingPerson(t, s, "google-sub-link-3b")

	outcome, err := s.PersonIdentities().LinkToExistingPerson(ctx, iss, sub, personA)
	require.NoError(t, err)
	require.Equal(t, store.LinkCreated, outcome)

	_, err = s.PersonIdentities().LinkToExistingPerson(ctx, iss, sub, personB)
	require.ErrorIs(t, err, store.ErrLinkedToOtherPerson)

	linkedID, found := identityRow(t, db, iss, sub)
	require.True(t, found)
	assert.Equal(t, personA, linkedID, "the existing row must still point at the original Person -- nothing merged, reassigned, or overwritten")
	assert.Equal(t, 1, identityRowCount(t, db, iss, sub), "no second row may exist for the pair")
}

// TestPersonIdentityStore_LinkToExistingPerson_ThenFindOrCreate_ResolvesLinkedPerson
// covers FR7 (first direction): once LinkToExistingPerson has written a row
// for a pair, FindOrCreateByIssSub for that same pair resolves to the
// linked Person with created == false -- the auto-provisioning branch is
// not taken.
func TestPersonIdentityStore_LinkToExistingPerson_ThenFindOrCreate_ResolvesLinkedPerson(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	const iss, sub = "https://accounts.google.test", "human-link-4"

	personID := newExistingPerson(t, s, "google-sub-link-4")

	outcome, err := s.PersonIdentities().LinkToExistingPerson(ctx, iss, sub, personID)
	require.NoError(t, err)
	require.Equal(t, store.LinkCreated, outcome)

	resolved, created, err := s.PersonIdentities().FindOrCreateByIssSub(ctx, iss, sub)
	require.NoError(t, err)
	assert.False(t, created, "a pair with an existing link row must never hit the auto-provisioning branch")
	assert.Equal(t, personID, resolved.ID)
}

// TestPersonIdentityStore_FindOrCreateThenLinkToDifferentPerson_Conflicts
// covers FR7 (other direction): a pair auto-provisioned FIRST by
// FindOrCreateByIssSub still resolves precedence correctly -- a later
// LinkToExistingPerson to a different Person hits FR8's conflict rather
// than merging the orphaned auto-provisioned Person into the requested one.
func TestPersonIdentityStore_FindOrCreateThenLinkToDifferentPerson_Conflicts(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	const iss, sub = "https://keycloak.example.test/realms/humans", "human-link-5"

	autoProvisioned, created, err := s.PersonIdentities().FindOrCreateByIssSub(ctx, iss, sub)
	require.NoError(t, err)
	require.True(t, created)

	otherPerson := newExistingPerson(t, s, "google-sub-link-5")

	_, err = s.PersonIdentities().LinkToExistingPerson(ctx, iss, sub, otherPerson)
	require.ErrorIs(t, err, store.ErrLinkedToOtherPerson, "linking an already auto-provisioned pair to a different Person must fail safe, not merge")

	linkedID, found := identityRow(t, db, iss, sub)
	require.True(t, found)
	assert.Equal(t, autoProvisioned.ID, linkedID)
	assert.Equal(t, 1, identityRowCount(t, db, iss, sub))
}

// TestPersonIdentityStore_LinkToExistingPerson_ConcurrentDifferentPersons_ExactlyOneRowWins
// is LinkToExistingPerson's analogue of FindOrCreateByIssSub's own
// concurrent-first-sight test: two concurrent calls for the same (iss, sub)
// pair, targeting two DIFFERENT Persons. Exactly one row must survive, and
// the loser must see ErrLinkedToOtherPerson -- never a raw pgx unique-
// violation error and never a silently-overwritten row.
func TestPersonIdentityStore_LinkToExistingPerson_ConcurrentDifferentPersons_ExactlyOneRowWins(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	const iss, sub = "https://accounts.google.test", "human-link-concurrent-ab"

	personA := newExistingPerson(t, s, "google-sub-link-concurrent-a")
	personB := newExistingPerson(t, s, "google-sub-link-concurrent-b")
	targets := [2]uuid.UUID{personA, personB}

	var wg sync.WaitGroup
	outcomes := make([]store.LinkOutcome, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outcomes[i], errs[i] = s.PersonIdentities().LinkToExistingPerson(ctx, iss, sub, targets[i])
		}(i)
	}
	wg.Wait()

	winners, losers := 0, 0
	for i, err := range errs {
		switch {
		case err == nil:
			assert.Equal(t, store.LinkCreated, outcomes[i], "goroutine %d", i)
			winners++
		case errors.Is(err, store.ErrLinkedToOtherPerson):
			losers++
		default:
			t.Fatalf("goroutine %d: unexpected error (want nil or ErrLinkedToOtherPerson, no raw pgx leak): %v", i, err)
		}
	}
	assert.Equal(t, 1, winners, "exactly one concurrent linker must win")
	assert.Equal(t, 1, losers, "the other concurrent linker must lose with ErrLinkedToOtherPerson")

	assert.Equal(t, 1, identityRowCount(t, db, iss, sub), "no duplicate rows may survive the race")
	linkedID, found := identityRow(t, db, iss, sub)
	require.True(t, found)
	assert.Contains(t, targets[:], linkedID, "the surviving row must point at one of the two contested Persons")
}

// TestPersonIdentityStore_LinkToExistingPerson_ConcurrentSamePerson_BothSucceedNoDuplicate
// is the same race with both concurrent callers targeting the SAME Person:
// per FR9, the loser must see LinkAlreadyOwned rather than
// ErrLinkedToOtherPerson, since there is no real conflict.
func TestPersonIdentityStore_LinkToExistingPerson_ConcurrentSamePerson_BothSucceedNoDuplicate(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	const iss, sub = "https://accounts.google.test", "human-link-concurrent-same"

	personID := newExistingPerson(t, s, "google-sub-link-concurrent-same")

	var wg sync.WaitGroup
	outcomes := make([]store.LinkOutcome, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outcomes[i], errs[i] = s.PersonIdentities().LinkToExistingPerson(ctx, iss, sub, personID)
		}(i)
	}
	wg.Wait()

	created, alreadyOwned := 0, 0
	for i, err := range errs {
		require.NoError(t, err, "goroutine %d: same-Person race must never error", i)
		switch outcomes[i] {
		case store.LinkCreated:
			created++
		case store.LinkAlreadyOwned:
			alreadyOwned++
		}
	}
	assert.Equal(t, 1, created)
	assert.Equal(t, 1, alreadyOwned)
	assert.Equal(t, 1, identityRowCount(t, db, iss, sub), "no duplicate rows may survive a same-Person race")
}

// TestPersonIdentityStore_LinkToExistingPerson_UnknownPersonID_FKError_NoRowWritten
// asserts LinkToExistingPerson never silently creates a Person: an unknown
// personID must fail on person_oidc_identity's foreign key, and no row may
// be written for the pair.
func TestPersonIdentityStore_LinkToExistingPerson_UnknownPersonID_FKError_NoRowWritten(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	const iss, sub = "https://accounts.google.test", "human-link-unknown-person"

	_, err := s.PersonIdentities().LinkToExistingPerson(ctx, iss, sub, uuid.New())
	require.Error(t, err)
	assert.NotErrorIs(t, err, store.ErrLinkedToOtherPerson, "an unknown personID is a foreign-key failure, not a conflict with an existing link")

	assert.Equal(t, 0, identityRowCount(t, db, iss, sub), "no row may be written when personID doesn't exist")
}

// TestPersonIdentityStore_LinkToExistingPerson_ContextCancelled_NoPartialRow
// covers NFR4: a call whose context is cancelled leaves no partial
// person_oidc_identity row behind -- the whole decide-and-write runs in one
// transaction, so a mid-flight failure rolls back in full rather than
// leaving a half-written link.
func TestPersonIdentityStore_LinkToExistingPerson_ContextCancelled_NoPartialRow(t *testing.T) {
	s, db := newStore(t)
	const iss, sub = "https://accounts.google.test", "human-link-cancelled"

	personID := newExistingPerson(t, s, "google-sub-link-cancelled")

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := s.PersonIdentities().LinkToExistingPerson(cancelledCtx, iss, sub, personID)
	require.Error(t, err, "a call against an already-cancelled context must fail rather than silently proceed")

	assert.Equal(t, 0, identityRowCount(t, db, iss, sub), "a cancelled call must leave no partial row")
}
