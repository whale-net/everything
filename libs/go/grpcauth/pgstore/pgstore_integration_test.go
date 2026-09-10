//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See the go_test target's gotags in BUILD.bazel for how to run it.
//
// These tests exercise exactly what pgstore_test.go's pure-Go unit tests
// cannot: a real PostgreSQL round trip, genuine ciphertext-at-rest (NFR2),
// and status-first enforcement (FR5/FR6/FR7/FR11) backed by actual SQL
// rather than an in-memory fake -- mirrors
// libs/go/mcpauth/credential_integration_test.go's pattern via
// libs/go/dbtest. Each test creates the expected
// grpcauth_delegated_grant-shaped table itself (no shipped migration --
// FR13; see pgstore.go's package doc for the schema contract).
package pgstore

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/grpcauth"
)

// grantSchema is a self-contained copy of the schema contract documented in
// pgstore.go's package doc comment. dbtest's own README asks integration
// tests to keep schema self-contained rather than importing another
// package's migrations.
const grantSchema = `
	CREATE TABLE grpcauth_delegated_grant (
		subject        TEXT        NOT NULL,
		grant_key      TEXT        NOT NULL,
		token_material BYTEA       NOT NULL,
		status         TEXT        NOT NULL,
		created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY (subject, grant_key)
	);
`

// testKey returns a fixed, valid grpcauth.GrantKeySize-byte encryption key
// for use across the integration tests.
func testKey() []byte {
	key := make([]byte, grpcauth.GrantKeySize)
	for i := range key {
		key[i] = byte(i + 1)
	}
	return key
}

// newTestStore stands up a real Postgres-backed grantStore with the
// grpcauth_delegated_grant schema already applied.
func newTestStore(ctx context.Context, t *testing.T) (grpcauth.Store, *dbtest.Postgres) {
	t.Helper()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: grantSchema})
	store, err := NewGrantStore(ctx, StoreConfig{
		Pool:          db.Pool,
		EncryptionKey: testKey(),
	})
	if err != nil {
		t.Fatalf("NewGrantStore: %v", err)
	}
	return store, db
}

// TestGrantStore_RoundTrip_PersistStatusTokenMaterial is the base round
// trip: Persist -> Status == active -> TokenMaterial returns the original
// refresh token.
func TestGrantStore_RoundTrip_PersistStatusTokenMaterial(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(ctx, t)

	material := grpcauth.TokenMaterial{RefreshToken: "rt-round-trip", ObtainedAt: time.Now()}
	if err := store.Persist(ctx, "alice", "grant-1", material); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	status, err := store.Status(ctx, "alice", "grant-1")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status != grpcauth.GrantStatusActive {
		t.Fatalf("Status = %q, want %q", status, grpcauth.GrantStatusActive)
	}

	got, err := store.TokenMaterial(ctx, "alice", "grant-1")
	if err != nil {
		t.Fatalf("TokenMaterial: %v", err)
	}
	if got.RefreshToken != material.RefreshToken {
		t.Fatalf("RefreshToken = %q, want %q", got.RefreshToken, material.RefreshToken)
	}
}

// TestGrantStore_NFR2_CiphertextAtRest_NotPlaintextAndPerWriteNonce reads
// the material column directly with raw SQL and asserts the stored bytes
// are not the plaintext refresh token and do not contain it as a
// substring, and that persisting the same plaintext twice yields different
// stored bytes (per-write nonce).
func TestGrantStore_NFR2_CiphertextAtRest_NotPlaintextAndPerWriteNonce(t *testing.T) {
	ctx := context.Background()
	store, db := newTestStore(ctx, t)

	const plaintext = "super-secret-refresh-token-value"
	material := grpcauth.TokenMaterial{RefreshToken: plaintext, ObtainedAt: time.Now()}
	if err := store.Persist(ctx, "erin", "grant-nfr2", material); err != nil {
		t.Fatalf("Persist (first write): %v", err)
	}

	var firstCiphertext []byte
	if err := db.Pool.QueryRow(ctx,
		"SELECT token_material FROM grpcauth_delegated_grant WHERE subject = $1 AND grant_key = $2",
		"erin", "grant-nfr2",
	).Scan(&firstCiphertext); err != nil {
		t.Fatalf("raw select (first): %v", err)
	}

	if bytes.Equal(firstCiphertext, []byte(plaintext)) {
		t.Fatal("stored token_material equals the plaintext refresh token verbatim")
	}
	if bytes.Contains(firstCiphertext, []byte(plaintext)) {
		t.Fatal("stored token_material contains the plaintext refresh token as a substring")
	}

	// Persist the same plaintext again: per-write nonce means the stored
	// bytes must differ even though the logical content is identical.
	if err := store.Persist(ctx, "erin", "grant-nfr2", material); err != nil {
		t.Fatalf("Persist (second write): %v", err)
	}

	var secondCiphertext []byte
	if err := db.Pool.QueryRow(ctx,
		"SELECT token_material FROM grpcauth_delegated_grant WHERE subject = $1 AND grant_key = $2",
		"erin", "grant-nfr2",
	).Scan(&secondCiphertext); err != nil {
		t.Fatalf("raw select (second): %v", err)
	}

	if bytes.Equal(firstCiphertext, secondCiphertext) {
		t.Fatal("two writes of the same plaintext produced identical ciphertext -- nonce is not per-write")
	}

	// Sanity: the store itself still decrypts correctly after the second
	// write.
	got, err := store.TokenMaterial(ctx, "erin", "grant-nfr2")
	if err != nil {
		t.Fatalf("TokenMaterial: %v", err)
	}
	if got.RefreshToken != plaintext {
		t.Fatalf("RefreshToken = %q, want %q", got.RefreshToken, plaintext)
	}
}

// TestGrantStore_FR7_RevokedAndNeedsReauth_ShortCircuitBeforeDecrypt asserts
// Revoke -> TokenMaterial returns ErrGrantRevoked and MarkNeedsReauth ->
// TokenMaterial returns ErrGrantNeedsReauth, in both cases proving the
// status check precedes decryption: the stored ciphertext is corrupted
// first (so a decrypt attempt would return a decrypt error, not a status
// sentinel), and the status error must still surface.
func TestGrantStore_FR7_RevokedAndNeedsReauth_ShortCircuitBeforeDecrypt(t *testing.T) {
	ctx := context.Background()
	store, db := newTestStore(ctx, t)

	// -- revoked --
	material := grpcauth.TokenMaterial{RefreshToken: "rt-fr7-revoked", ObtainedAt: time.Now()}
	if err := store.Persist(ctx, "frank", "grant-revoked", material); err != nil {
		t.Fatalf("Persist (revoked case): %v", err)
	}
	corruptCiphertext(ctx, t, db, "frank", "grant-revoked")
	if err := store.Revoke(ctx, "frank", "grant-revoked"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	_, err := store.TokenMaterial(ctx, "frank", "grant-revoked")
	if !errors.Is(err, grpcauth.ErrGrantRevoked) {
		t.Fatalf("TokenMaterial after Revoke + corrupted ciphertext: err = %v, want ErrGrantRevoked (status check must precede decrypt)", err)
	}

	// -- needs_reauth --
	material2 := grpcauth.TokenMaterial{RefreshToken: "rt-fr7-needs-reauth", ObtainedAt: time.Now()}
	if err := store.Persist(ctx, "frank", "grant-needs-reauth", material2); err != nil {
		t.Fatalf("Persist (needs_reauth case): %v", err)
	}
	corruptCiphertext(ctx, t, db, "frank", "grant-needs-reauth")
	if err := store.MarkNeedsReauth(ctx, "frank", "grant-needs-reauth"); err != nil {
		t.Fatalf("MarkNeedsReauth: %v", err)
	}
	_, err = store.TokenMaterial(ctx, "frank", "grant-needs-reauth")
	if !errors.Is(err, grpcauth.ErrGrantNeedsReauth) {
		t.Fatalf("TokenMaterial after MarkNeedsReauth + corrupted ciphertext: err = %v, want ErrGrantNeedsReauth (status check must precede decrypt)", err)
	}
}

// corruptCiphertext overwrites the stored token_material for (subject,
// grant) with garbage bytes that cannot successfully decrypt, via raw SQL
// bypassing the store entirely.
func corruptCiphertext(ctx context.Context, t *testing.T, db *dbtest.Postgres, subject, grant string) {
	t.Helper()
	if _, err := db.Pool.Exec(ctx,
		"UPDATE grpcauth_delegated_grant SET token_material = $1 WHERE subject = $2 AND grant_key = $3",
		[]byte("not-valid-gcm-ciphertext-at-all"), subject, grant,
	); err != nil {
		t.Fatalf("corruptCiphertext: %v", err)
	}
}

// TestGrantStore_FR5_RevokeIsolatesOtherGrantsOfSameSubject asserts
// Revoke(alice, s1) leaves alice's s2 grant active and usable.
func TestGrantStore_FR5_RevokeIsolatesOtherGrantsOfSameSubject(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(ctx, t)

	m1 := grpcauth.TokenMaterial{RefreshToken: "rt-s1", ObtainedAt: time.Now()}
	m2 := grpcauth.TokenMaterial{RefreshToken: "rt-s2", ObtainedAt: time.Now()}
	if err := store.Persist(ctx, "alice", "s1", m1); err != nil {
		t.Fatalf("Persist s1: %v", err)
	}
	if err := store.Persist(ctx, "alice", "s2", m2); err != nil {
		t.Fatalf("Persist s2: %v", err)
	}

	if err := store.Revoke(ctx, "alice", "s1"); err != nil {
		t.Fatalf("Revoke s1: %v", err)
	}

	if _, err := store.TokenMaterial(ctx, "alice", "s1"); !errors.Is(err, grpcauth.ErrGrantRevoked) {
		t.Fatalf("TokenMaterial(alice, s1) after revoke: err = %v, want ErrGrantRevoked", err)
	}

	status2, err := store.Status(ctx, "alice", "s2")
	if err != nil {
		t.Fatalf("Status(alice, s2): %v", err)
	}
	if status2 != grpcauth.GrantStatusActive {
		t.Fatalf("Status(alice, s2) = %q, want %q (must be untouched by revoking s1)", status2, grpcauth.GrantStatusActive)
	}

	got2, err := store.TokenMaterial(ctx, "alice", "s2")
	if err != nil {
		t.Fatalf("TokenMaterial(alice, s2): %v", err)
	}
	if got2.RefreshToken != m2.RefreshToken {
		t.Fatalf("TokenMaterial(alice, s2).RefreshToken = %q, want %q", got2.RefreshToken, m2.RefreshToken)
	}
}

// TestGrantStore_FR6_CallerAgnosticRevoke asserts Revoke(bob, s1), invoked
// with no notion of a "current caller" at all (the Store interface has no
// such parameter to begin with), produces the identical unconditional-write
// + TokenMaterial-short-circuit behaviour as the FR5 self-service case, and
// does not affect bob's other grants or alice's grants.
func TestGrantStore_FR6_CallerAgnosticRevoke(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(ctx, t)

	if err := store.Persist(ctx, "alice", "s1", grpcauth.TokenMaterial{RefreshToken: "rt-alice-s1", ObtainedAt: time.Now()}); err != nil {
		t.Fatalf("Persist alice/s1: %v", err)
	}
	if err := store.Persist(ctx, "bob", "s1", grpcauth.TokenMaterial{RefreshToken: "rt-bob-s1", ObtainedAt: time.Now()}); err != nil {
		t.Fatalf("Persist bob/s1: %v", err)
	}
	if err := store.Persist(ctx, "bob", "s2", grpcauth.TokenMaterial{RefreshToken: "rt-bob-s2", ObtainedAt: time.Now()}); err != nil {
		t.Fatalf("Persist bob/s2: %v", err)
	}

	// Revoke is called with only the explicit (subject, grant) pair --
	// there is no "acting as" identity parameter anywhere on the Store
	// interface to omit or check.
	if err := store.Revoke(ctx, "bob", "s1"); err != nil {
		t.Fatalf("Revoke(bob, s1): %v", err)
	}

	if _, err := store.TokenMaterial(ctx, "bob", "s1"); !errors.Is(err, grpcauth.ErrGrantRevoked) {
		t.Fatalf("TokenMaterial(bob, s1) after revoke: err = %v, want ErrGrantRevoked", err)
	}

	bobS2Status, err := store.Status(ctx, "bob", "s2")
	if err != nil {
		t.Fatalf("Status(bob, s2): %v", err)
	}
	if bobS2Status != grpcauth.GrantStatusActive {
		t.Fatalf("Status(bob, s2) = %q, want %q (bob's other grant must be untouched)", bobS2Status, grpcauth.GrantStatusActive)
	}

	aliceS1Status, err := store.Status(ctx, "alice", "s1")
	if err != nil {
		t.Fatalf("Status(alice, s1): %v", err)
	}
	if aliceS1Status != grpcauth.GrantStatusActive {
		t.Fatalf("Status(alice, s1) = %q, want %q (a different subject's grant must be untouched)", aliceS1Status, grpcauth.GrantStatusActive)
	}
}

// TestGrantStore_FR11_ReauthThenPersist_ReusesSameRow asserts
// MarkNeedsReauth then Persist with new material yields Status == active
// again, the new material, and an unchanged row count for that subject
// (same key reused, no new grant identity).
func TestGrantStore_FR11_ReauthThenPersist_ReusesSameRow(t *testing.T) {
	ctx := context.Background()
	store, db := newTestStore(ctx, t)

	original := grpcauth.TokenMaterial{RefreshToken: "rt-fr11-old", ObtainedAt: time.Now()}
	if err := store.Persist(ctx, "grace", "grant-fr11", original); err != nil {
		t.Fatalf("Persist (initial): %v", err)
	}
	if err := store.MarkNeedsReauth(ctx, "grace", "grant-fr11"); err != nil {
		t.Fatalf("MarkNeedsReauth: %v", err)
	}

	var rowCountBefore int
	if err := db.Pool.QueryRow(ctx,
		"SELECT count(*) FROM grpcauth_delegated_grant WHERE subject = $1", "grace",
	).Scan(&rowCountBefore); err != nil {
		t.Fatalf("count before: %v", err)
	}

	reconsented := grpcauth.TokenMaterial{RefreshToken: "rt-fr11-new", ObtainedAt: time.Now()}
	if err := store.Persist(ctx, "grace", "grant-fr11", reconsented); err != nil {
		t.Fatalf("Persist (re-consent): %v", err)
	}

	status, err := store.Status(ctx, "grace", "grant-fr11")
	if err != nil {
		t.Fatalf("Status after re-consent: %v", err)
	}
	if status != grpcauth.GrantStatusActive {
		t.Fatalf("Status after re-consent = %q, want %q", status, grpcauth.GrantStatusActive)
	}

	got, err := store.TokenMaterial(ctx, "grace", "grant-fr11")
	if err != nil {
		t.Fatalf("TokenMaterial after re-consent: %v", err)
	}
	if got.RefreshToken != reconsented.RefreshToken {
		t.Fatalf("RefreshToken = %q, want %q", got.RefreshToken, reconsented.RefreshToken)
	}

	var rowCountAfter int
	if err := db.Pool.QueryRow(ctx,
		"SELECT count(*) FROM grpcauth_delegated_grant WHERE subject = $1", "grace",
	).Scan(&rowCountAfter); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if rowCountAfter != rowCountBefore {
		t.Fatalf("row count for subject changed across re-consent: before = %d, after = %d, want unchanged (same key reused)", rowCountBefore, rowCountAfter)
	}
	if rowCountAfter != 1 {
		t.Fatalf("row count for subject = %d, want exactly 1", rowCountAfter)
	}
}

// TestGrantStore_UnknownGrant_ReturnsNotFound asserts Status,
// TokenMaterial, and Revoke on an unknown (subject, grant) all return
// ErrGrantNotFound without panicking.
func TestGrantStore_UnknownGrant_ReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(ctx, t)

	if _, err := store.Status(ctx, "nobody", "nothing"); !errors.Is(err, grpcauth.ErrGrantNotFound) {
		t.Fatalf("Status error = %v, want ErrGrantNotFound", err)
	}
	if _, err := store.TokenMaterial(ctx, "nobody", "nothing"); !errors.Is(err, grpcauth.ErrGrantNotFound) {
		t.Fatalf("TokenMaterial error = %v, want ErrGrantNotFound", err)
	}
	if err := store.Revoke(ctx, "nobody", "nothing"); !errors.Is(err, grpcauth.ErrGrantNotFound) {
		t.Fatalf("Revoke error = %v, want ErrGrantNotFound", err)
	}
	if err := store.MarkNeedsReauth(ctx, "nobody", "nothing"); !errors.Is(err, grpcauth.ErrGrantNotFound) {
		t.Fatalf("MarkNeedsReauth error = %v, want ErrGrantNotFound", err)
	}
}

// TestGrantStore_MultiInstance_SharedPool asserts Persist via one
// *grantStore and a read via a second one built on the same pool observe
// the same data (mirrors mcpauth's multi-replica coverage).
func TestGrantStore_MultiInstance_SharedPool(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: grantSchema})
	key := testKey()

	storeA, err := NewGrantStore(ctx, StoreConfig{Pool: db.Pool, EncryptionKey: key})
	if err != nil {
		t.Fatalf("NewGrantStore (A): %v", err)
	}
	storeB, err := NewGrantStore(ctx, StoreConfig{Pool: db.Pool, EncryptionKey: key})
	if err != nil {
		t.Fatalf("NewGrantStore (B): %v", err)
	}

	material := grpcauth.TokenMaterial{RefreshToken: "rt-multi-instance", ObtainedAt: time.Now()}
	if err := storeA.Persist(ctx, "heidi", "grant-multi", material); err != nil {
		t.Fatalf("Persist via instance A: %v", err)
	}

	got, err := storeB.TokenMaterial(ctx, "heidi", "grant-multi")
	if err != nil {
		t.Fatalf("TokenMaterial via instance B: %v", err)
	}
	if got.RefreshToken != material.RefreshToken {
		t.Fatalf("RefreshToken via instance B = %q, want %q", got.RefreshToken, material.RefreshToken)
	}

	// Read-your-write in the other direction too, to prove this isn't an
	// artifact of read ordering.
	if err := storeB.Revoke(ctx, "heidi", "grant-multi"); err != nil {
		t.Fatalf("Revoke via instance B: %v", err)
	}
	if _, err := storeA.TokenMaterial(ctx, "heidi", "grant-multi"); !errors.Is(err, grpcauth.ErrGrantRevoked) {
		t.Fatalf("TokenMaterial via instance A after Revoke via instance B: err = %v, want ErrGrantRevoked", err)
	}
}

// TestGrantStore_UnrecognizedStatusValue_RefusesToGuess asserts that a row
// whose persisted status column holds a value outside the three defined
// grpcauth.GrantStatus values (simulating corruption or a schema/version
// mismatch) causes TokenMaterial to return an error rather than silently
// treating it as active and handing back decrypted material.
func TestGrantStore_UnrecognizedStatusValue_RefusesToGuess(t *testing.T) {
	ctx := context.Background()
	store, db := newTestStore(ctx, t)

	if err := store.Persist(ctx, "ivan", "grant-weird-status", grpcauth.TokenMaterial{RefreshToken: "rt-weird", ObtainedAt: time.Now()}); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		"UPDATE grpcauth_delegated_grant SET status = 'not-a-real-status' WHERE subject = $1 AND grant_key = $2",
		"ivan", "grant-weird-status",
	); err != nil {
		t.Fatalf("corrupt status: %v", err)
	}

	_, err := store.TokenMaterial(ctx, "ivan", "grant-weird-status")
	if err == nil {
		t.Fatal("TokenMaterial with an unrecognized persisted status: got nil error, want error")
	}
	if errors.Is(err, grpcauth.ErrGrantRevoked) || errors.Is(err, grpcauth.ErrGrantNeedsReauth) || errors.Is(err, grpcauth.ErrGrantNotFound) {
		t.Fatalf("TokenMaterial with an unrecognized persisted status matched a defined sentinel: %v", err)
	}
	if !strings.Contains(err.Error(), "not-a-real-status") {
		t.Fatalf("error = %q, want it to name the unrecognized status value", err.Error())
	}
}
