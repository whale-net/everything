//go:build integration

// Real-Postgres coverage for PersonIdentityStore.FindOrCreateByIssSub
// (migration 020, issue #2116, FR12(b)) -- the (iss, sub) -> Person
// auto-provisioning find-or-create this package's whagent-net
// authentication path resolves every verified whagent Claim against. See
// store_integration_test.go's package doc for why this file only builds
// under the "integration" build tag.
package store_test

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
