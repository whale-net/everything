//go:build integration

// This file only builds under the "integration" build tag — see
// credential_integration_test.go's header comment for the full rationale
// (mirrored here for the auth-code store instead of the credential store).
//
// These tests exercise exactly what authorize_test.go/token_test.go's
// pure-Go unit tests cannot (they only ever exercise memoryAuthCodeStore):
// real PostgreSQL preflight failure/success against a real mcp_auth_code
// -shaped table, an authorization code Saved via one *pgxAuthCodeStore
// instance ("replica A") being Consumed via a second, separately
// constructed instance sharing the same database ("replica B" — the
// cross-replica guarantee memoryAuthCodeStore cannot give, and the whole
// reason #1642's README warns a multi-replica deployment MUST use this
// store), single-use enforcement under real concurrent load (Postgres's own
// DELETE ... RETURNING atomicity, not an in-process mutex), and expired-row
// pruning enforced by actual SQL rather than an in-memory map sweep.
package mcpauth

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/dbtest"
)

const authCodeSchema = `
	CREATE TABLE mcp_auth_code (
		code_hash             TEXT        PRIMARY KEY,
		client_id             TEXT        NOT NULL,
		redirect_uri          TEXT        NOT NULL,
		identity              TEXT        NOT NULL,
		code_challenge        TEXT        NOT NULL,
		code_challenge_method TEXT        NOT NULL,
		expires_at            TIMESTAMPTZ NOT NULL,
		created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);
	CREATE INDEX mcp_auth_code_expires_at ON mcp_auth_code(expires_at);
`

// testAuthCode returns a well-formed AuthCode (unhashed Code field — the
// caller is expected to hash it exactly like authorize.go does before
// calling Save) with a TTL far enough in the future not to interfere with
// tests that aren't specifically about expiry.
func testAuthCode(rawCode string) AuthCode {
	return AuthCode{
		Code:                hashToken(rawCode),
		ClientID:            "client-1",
		RedirectURI:         "https://client.example.com/callback",
		Identity:            "person-1",
		CodeChallenge:       "fixed-challenge-value",
		CodeChallengeMethod: "S256",
		ExpiresAt:           time.Now().Add(time.Minute),
	}
}

// ── preflight ────────────────────────────────────────────────────────────

func TestAuthCodePreflight_MissingTable_FailsNamingTable(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{
		// No Schema — mcp_auth_code does not exist.
	})

	store, err := NewPostgresAuthCodeStore(ctx, AuthCodeStoreConfig{Pool: db.Pool})

	require.Error(t, err, "preflight must fail when mcp_auth_code does not exist")
	assert.Nil(t, store, "on preflight failure, no AuthCodeStore must be handed back")
	assert.Contains(t, err.Error(), "mcp_auth_code", "error must name the table")
}

func TestAuthCodePreflight_PresentTable_BootProceeds(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: authCodeSchema})

	store, err := NewPostgresAuthCodeStore(ctx, AuthCodeStoreConfig{Pool: db.Pool})

	require.NoError(t, err)
	require.NotNil(t, store)
}

// ── multi-replica round trip ─────────────────────────────────────────────

// TestPostgresAuthCodeStore_MultiReplica_SaveOnOneConsumeOnAnother proves the
// guarantee memoryAuthCodeStore cannot give: a code Saved via one
// *pgxAuthCodeStore instance ("replica A", standing in for the replica that
// served GET /authorize) is Consumable via a second, entirely separate
// instance ("replica B", standing in for the replica that serves POST
// /token) sharing the same underlying database.
func TestPostgresAuthCodeStore_MultiReplica_SaveOnOneConsumeOnAnother(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: authCodeSchema})

	replicaA, err := NewPostgresAuthCodeStore(ctx, AuthCodeStoreConfig{Pool: db.Pool})
	require.NoError(t, err)
	replicaB, err := NewPostgresAuthCodeStore(ctx, AuthCodeStoreConfig{Pool: db.Pool})
	require.NoError(t, err)

	const rawCode = "cross-replica-raw-code"
	want := testAuthCode(rawCode)
	require.NoError(t, replicaA.Save(ctx, want))

	got, err := replicaB.Consume(ctx, rawCode)
	require.NoError(t, err)
	assert.Equal(t, want.ClientID, got.ClientID)
	assert.Equal(t, want.RedirectURI, got.RedirectURI)
	assert.Equal(t, want.Identity, got.Identity)
	assert.Equal(t, want.CodeChallenge, got.CodeChallenge)
	assert.Equal(t, want.CodeChallengeMethod, got.CodeChallengeMethod)

	// And the code is now gone from either replica's view — single-use is a
	// database-level guarantee, not scoped to whichever instance consumed it.
	_, err = replicaA.Consume(ctx, rawCode)
	assert.ErrorIs(t, err, ErrAuthCodeNotFound)
}

func TestPostgresAuthCodeStore_Consume_UnknownCode_ReturnsErrAuthCodeNotFound(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: authCodeSchema})

	store, err := NewPostgresAuthCodeStore(ctx, AuthCodeStoreConfig{Pool: db.Pool})
	require.NoError(t, err)

	_, err = store.Consume(ctx, "never-issued-code")
	assert.ErrorIs(t, err, ErrAuthCodeNotFound)
}

// ── single-use under concurrency ────────────────────────────────────────

// TestPostgresAuthCodeStore_ConcurrentConsume_ExactlyOneSucceeds fires N
// goroutines at Consume for the exact same code. Exactly one must succeed;
// every other must come back ErrAuthCodeNotFound — proving single-use is
// enforced by Postgres's DELETE ... RETURNING row-lock semantics, not an
// in-process mutex that only memoryAuthCodeStore could ever offer.
func TestPostgresAuthCodeStore_ConcurrentConsume_ExactlyOneSucceeds(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: authCodeSchema})

	store, err := NewPostgresAuthCodeStore(ctx, AuthCodeStoreConfig{Pool: db.Pool})
	require.NoError(t, err)

	const rawCode = "racy-code"
	require.NoError(t, store.Save(ctx, testAuthCode(rawCode)))

	const workers = 20
	var wg sync.WaitGroup
	successes := make([]bool, workers)
	errs := make([]error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := store.Consume(ctx, rawCode)
			errs[i] = err
			successes[i] = err == nil
		}(i)
	}
	wg.Wait()

	successCount := 0
	for i, ok := range successes {
		if ok {
			successCount++
		} else {
			assert.ErrorIs(t, errs[i], ErrAuthCodeNotFound, "every losing Consume must report the code as not found, not some other error")
		}
	}
	assert.Equal(t, 1, successCount, "exactly one concurrent Consume of the same code must succeed")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM mcp_auth_code").Scan(&count))
	assert.Equal(t, 0, count, "the winning Consume must delete the row, leaving none behind")
}

// ── expiry pruning ──────────────────────────────────────────────────────

// TestPostgresAuthCodeStore_PruneExpired_DeletesExpiredRowsOnAccess proves
// pgxAuthCodeStore.pruneExpired actually deletes rows past expires_at from
// the real table — this store's "cannot grow unbounded" claim only holds if
// the DELETE really runs, and a pure-Go unit test using a fake clock/store
// cannot exercise the real SQL doing it.
//
// Both rows are Saved with a future expires_at (Save itself calls
// pruneExpired before inserting, so a row Saved already-expired would never
// survive its own insert) and the "expired" row is then aged past its
// expiry directly via SQL — simulating time passing without racing the
// store's own prune-on-Save behavior.
func TestPostgresAuthCodeStore_PruneExpired_DeletesExpiredRowsOnAccess(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: authCodeSchema})

	store, err := NewPostgresAuthCodeStore(ctx, AuthCodeStoreConfig{Pool: db.Pool})
	require.NoError(t, err)

	toExpire := testAuthCode("about-to-expire-code")
	require.NoError(t, store.Save(ctx, toExpire))

	live := testAuthCode("live-code")
	require.NoError(t, store.Save(ctx, live))

	var countBefore int
	require.NoError(t, db.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM mcp_auth_code").Scan(&countBefore))
	require.Equal(t, 2, countBefore, "both rows must be persisted before either is aged past expiry")

	// Age the first row past expiry directly via SQL, bypassing the store
	// (which would otherwise prune it on the way in).
	_, err = db.Pool.Exec(ctx, "UPDATE mcp_auth_code SET expires_at = $1 WHERE code_hash = $2",
		time.Now().Add(-time.Minute), toExpire.Code)
	require.NoError(t, err)

	// Consume of the live code is itself an access that triggers
	// pruneExpired first — proving the now-expired row is swept as a side
	// effect of ordinary traffic, not just a dedicated cleanup call.
	_, err = store.Consume(ctx, "live-code")
	require.NoError(t, err)

	var countAfter int
	require.NoError(t, db.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM mcp_auth_code").Scan(&countAfter))
	assert.Equal(t, 0, countAfter, "the aged-out row must be pruned and the live row must be gone too (consumed)")

	// The pruned code is now unreachable via Consume — it is gone, not just
	// stale.
	_, err = store.Consume(ctx, "about-to-expire-code")
	assert.ErrorIs(t, err, ErrAuthCodeNotFound)
}
