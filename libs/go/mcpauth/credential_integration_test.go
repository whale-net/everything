//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs it.
// See the go_test target's gotags in BUILD.bazel and libs/go/dbtest/README.md
// for how to run it.
//
// These tests exercise exactly what credential_test.go's pure-Go unit tests
// cannot: real PostgreSQL preflight failure/success against a real
// mcp_credential-shaped table, real search_path resolution, real
// INSERT/UPDATE/SELECT round trips (including the StoreConfig.IdentityCast
// path against a genuine `uuid` column), and idempotent-revoke/no-leak
// behavior enforced by actual SQL rather than an in-memory fake.
//
// Two schema shapes are covered, matching StoreConfig.IdentityCast's two
// intended use cases (see README.md "Identity column and casting"):
//   - a generic consumer, identity TEXT, no cast needed
//   - an ASS-shaped consumer, person_id UUID NOT NULL REFERENCES person(id),
//     proving IdentityCast against a genuine uuid column end to end
//
// Schema here is a self-contained copy of the shape README.md documents as
// the schema contract — dbtest's own README asks integration tests to keep
// schema self-contained rather than importing another package's migrations.
package mcpauth

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/dbtest"
)

const genericCredentialSchema = `
	CREATE TABLE mcp_credential (
		id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
		identity     TEXT        NOT NULL,
		token_hash   TEXT        NOT NULL UNIQUE,
		created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_used_at TIMESTAMPTZ,
		revoked_at   TIMESTAMPTZ
	);
	CREATE INDEX mcp_credential_token_hash ON mcp_credential(token_hash) WHERE revoked_at IS NULL;
	CREATE INDEX mcp_credential_identity ON mcp_credential(identity);
`

const assShapedCredentialSchema = `
	CREATE TABLE person (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid()
	);
	CREATE TABLE mcp_credential (
		id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
		person_id    UUID        NOT NULL REFERENCES person(id),
		token_hash   TEXT        NOT NULL UNIQUE,
		created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_used_at TIMESTAMPTZ,
		revoked_at   TIMESTAMPTZ
	);
	CREATE INDEX mcp_credential_token_hash ON mcp_credential(token_hash) WHERE revoked_at IS NULL;
	CREATE INDEX mcp_credential_person_id ON mcp_credential(person_id);
`

// ── preflight ────────────────────────────────────────────────────────────

func TestPreflight_MissingTable_FailsNamingTable(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{
		// No Schema — mcp_credential does not exist.
	})

	store, err := NewCredentialStore(ctx, StoreConfig{Pool: db.Pool})

	require.Error(t, err, "preflight must fail when mcp_credential does not exist")
	assert.Nil(t, store, "on preflight failure, no CredentialStore must be handed back")
	assert.Contains(t, err.Error(), "mcp_credential", "error must name the table")
}

func TestPreflight_PresentTable_BootProceeds(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: genericCredentialSchema})

	store, err := NewCredentialStore(ctx, StoreConfig{Pool: db.Pool})

	require.NoError(t, err)
	require.NotNil(t, store)
}

// TestPreflight_TableOutsideSearchPath_Fails proves the probe uses the
// unqualified table name: a mcp_credential table that exists only in a
// schema not on the connection's search_path must fail preflight exactly
// like a missing table.
func TestPreflight_TableOutsideSearchPath_Fails(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: `
			CREATE SCHEMA other_schema;
			CREATE TABLE other_schema.mcp_credential (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid()
			);
		`,
	})

	store, err := NewCredentialStore(ctx, StoreConfig{Pool: db.Pool})

	require.Error(t, err, "a table outside search_path must fail preflight just like a missing table")
	assert.Nil(t, store)
}

// ── generic (identity TEXT) lifecycle ──────────────────────────────────

func TestGeneric_MintVerifyRevokeList_Lifecycle(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: genericCredentialSchema})

	store, err := NewCredentialStore(ctx, StoreConfig{Pool: db.Pool})
	require.NoError(t, err)

	const identity = "svc-account-1"

	rawToken, minted, err := store.Mint(ctx, identity)
	require.NoError(t, err)
	assert.NotEmpty(t, rawToken)
	assert.Equal(t, identity, minted.Identity)
	assert.Nil(t, minted.LastUsedAt)
	assert.Nil(t, minted.RevokedAt)

	// The raw token must not be recoverable from storage — only its hash is
	// persisted (NFR1).
	var storedHash string
	require.NoError(t, db.Pool.QueryRow(ctx, "SELECT token_hash FROM mcp_credential WHERE id = $1", minted.ID).Scan(&storedHash))
	assert.Equal(t, hashToken(rawToken), storedHash)
	assert.NotEqual(t, rawToken, storedHash, "the raw token must never be the stored value")

	// verify returns the minting identity and stamps last_used_at.
	gotIdentity, verified, err := store.Verify(ctx, rawToken)
	require.NoError(t, err)
	assert.Equal(t, identity, gotIdentity)
	require.NotNil(t, verified.LastUsedAt)
	firstLastUsed := *verified.LastUsedAt

	// a second verify moves last_used_at forward (or at least does not go
	// backward — real clocks can tie at test speed, so assert >=).
	_, verified2, err := store.Verify(ctx, rawToken)
	require.NoError(t, err)
	require.NotNil(t, verified2.LastUsedAt)
	assert.True(t, !verified2.LastUsedAt.Before(firstLastUsed), "second verify's last_used_at must not move backward")

	// revoke then verify fails.
	require.NoError(t, store.Revoke(ctx, minted.ID, identity))
	_, _, err = store.Verify(ctx, rawToken)
	assert.ErrorIs(t, err, ErrInvalidCredential)

	// revoke again is idempotent (no error).
	require.NoError(t, store.Revoke(ctx, minted.ID, identity))

	// list returns the credential, revoked.
	creds, err := store.List(ctx, identity)
	require.NoError(t, err)
	require.Len(t, creds, 1)
	assert.NotNil(t, creds[0].RevokedAt)
}

func TestGeneric_Verify_UnknownMalformedAndRevoked_AllFailIdentically(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: genericCredentialSchema})

	store, err := NewCredentialStore(ctx, StoreConfig{Pool: db.Pool})
	require.NoError(t, err)

	const identity = "svc-account-2"
	rawToken, minted, err := store.Mint(ctx, identity)
	require.NoError(t, err)
	require.NoError(t, store.Revoke(ctx, minted.ID, identity))

	_, _, unknownErr := store.Verify(ctx, "0000000000000000000000000000000000000000000000000000000000000000")
	_, _, malformedErr := store.Verify(ctx, "not-even-hex!!")
	_, _, revokedErr := store.Verify(ctx, rawToken)

	assert.ErrorIs(t, unknownErr, ErrInvalidCredential)
	assert.ErrorIs(t, malformedErr, ErrInvalidCredential)
	assert.ErrorIs(t, revokedErr, ErrInvalidCredential)
	assert.Equal(t, unknownErr.Error(), malformedErr.Error())
	assert.Equal(t, unknownErr.Error(), revokedErr.Error())
}

func TestGeneric_Revoke_WrongIdentity_IsNoOpCredentialStillVerifies(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: genericCredentialSchema})

	store, err := NewCredentialStore(ctx, StoreConfig{Pool: db.Pool})
	require.NoError(t, err)

	rawToken, minted, err := store.Mint(ctx, "owner-identity")
	require.NoError(t, err)

	// Revoke as a different identity: must be a silent no-op, not an error.
	require.NoError(t, store.Revoke(ctx, minted.ID, "someone-else"))

	_, _, verifyErr := store.Verify(ctx, rawToken)
	assert.NoError(t, verifyErr, "credential must still verify after a not-owned revoke attempt")
}

func TestGeneric_List_ReturnsLiveAndRevoked_MostRecentFirst(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: genericCredentialSchema})

	store, err := NewCredentialStore(ctx, StoreConfig{Pool: db.Pool})
	require.NoError(t, err)

	const identity = "svc-account-3"
	_, first, err := store.Mint(ctx, identity)
	require.NoError(t, err)
	_, second, err := store.Mint(ctx, identity)
	require.NoError(t, err)
	require.NoError(t, store.Revoke(ctx, first.ID, identity))

	creds, err := store.List(ctx, identity)
	require.NoError(t, err)
	require.Len(t, creds, 2)
	assert.Equal(t, second.ID, creds[0].ID, "most recently minted credential must come first")
	assert.Equal(t, first.ID, creds[1].ID)
	assert.NotNil(t, creds[1].RevokedAt)
	assert.Nil(t, creds[0].RevokedAt)
}

// TestGeneric_List_IsolatesAcrossIdentities proves List(identity) never
// leaks another identity's credentials — cross-identity isolation, not just
// cross-identity revoke (TestGeneric_Revoke_WrongIdentity_IsNoOpCredentialStillVerifies
// covers the write side; this covers the read side).
func TestGeneric_List_IsolatesAcrossIdentities(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: genericCredentialSchema})

	store, err := NewCredentialStore(ctx, StoreConfig{Pool: db.Pool})
	require.NoError(t, err)

	const identityA = "identity-a"
	const identityB = "identity-b"

	_, credA, err := store.Mint(ctx, identityA)
	require.NoError(t, err)
	_, credB1, err := store.Mint(ctx, identityB)
	require.NoError(t, err)
	_, credB2, err := store.Mint(ctx, identityB)
	require.NoError(t, err)
	require.NoError(t, store.Revoke(ctx, credB1.ID, identityB))

	listA, err := store.List(ctx, identityA)
	require.NoError(t, err)
	require.Len(t, listA, 1, "identity A's list must only contain identity A's credential")
	assert.Equal(t, credA.ID, listA[0].ID)

	listB, err := store.List(ctx, identityB)
	require.NoError(t, err)
	require.Len(t, listB, 2, "identity B's list must only contain identity B's credentials (live + revoked)")
	gotIDs := map[uuid.UUID]bool{listB[0].ID: true, listB[1].ID: true}
	assert.True(t, gotIDs[credB1.ID])
	assert.True(t, gotIDs[credB2.ID])
	assert.False(t, gotIDs[credA.ID], "identity B's list must not contain identity A's credential")

	// An identity that never minted anything gets an empty (not erroring)
	// list.
	listC, err := store.List(ctx, "identity-c-never-minted")
	require.NoError(t, err)
	assert.Empty(t, listC)
}

// TestGeneric_ConcurrentRevoke_SameCredential_IsSafeAndIdempotent fires many
// concurrent Revoke calls at the same credential/identity pair. Every call
// must return nil (idempotent, FR7) and the credential must end up revoked
// exactly once with no error, panic, or lost update under concurrency.
func TestGeneric_ConcurrentRevoke_SameCredential_IsSafeAndIdempotent(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: genericCredentialSchema})

	store, err := NewCredentialStore(ctx, StoreConfig{Pool: db.Pool})
	require.NoError(t, err)

	const identity = "svc-account-concurrent"
	rawToken, minted, err := store.Mint(ctx, identity)
	require.NoError(t, err)

	const workers = 20
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			errs <- store.Revoke(ctx, minted.ID, identity)
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		assert.NoError(t, err, "every concurrent Revoke call must succeed (idempotent, no error)")
	}

	// The credential must be revoked exactly once (no double-processing) and
	// verify must now fail.
	creds, err := store.List(ctx, identity)
	require.NoError(t, err)
	require.Len(t, creds, 1)
	assert.NotNil(t, creds[0].RevokedAt)

	_, _, verifyErr := store.Verify(ctx, rawToken)
	assert.ErrorIs(t, verifyErr, ErrInvalidCredential)
}

// TestGeneric_ConcurrentVerify_SameToken_AllSucceed proves concurrent
// Verify calls against the same live token all succeed (the
// last_used_at-stamping UPDATE must not lock out concurrent readers/writers
// of the same row).
func TestGeneric_ConcurrentVerify_SameToken_AllSucceed(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: genericCredentialSchema})

	store, err := NewCredentialStore(ctx, StoreConfig{Pool: db.Pool})
	require.NoError(t, err)

	const identity = "svc-account-concurrent-verify"
	rawToken, _, err := store.Mint(ctx, identity)
	require.NoError(t, err)

	const workers = 20
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			_, _, err := store.Verify(ctx, rawToken)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		assert.NoError(t, err, "every concurrent Verify call against a live token must succeed")
	}
}

// ── ASS-shaped (person_id UUID) lifecycle: proves IdentityCast ─────────

func TestASSShaped_IdentityCast_MintVerifyRevokeList_Lifecycle(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: assShapedCredentialSchema})

	var personID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, "INSERT INTO person DEFAULT VALUES RETURNING id").Scan(&personID))

	store, err := NewCredentialStore(ctx, StoreConfig{
		Pool:           db.Pool,
		IdentityColumn: "person_id",
		IdentityCast:   "uuid",
	})
	require.NoError(t, err)

	identity := personID.String()

	rawToken, minted, err := store.Mint(ctx, identity)
	require.NoError(t, err)
	assert.Equal(t, identity, minted.Identity)

	gotIdentity, verified, err := store.Verify(ctx, rawToken)
	require.NoError(t, err)
	assert.Equal(t, identity, gotIdentity)
	require.NotNil(t, verified.LastUsedAt)

	require.NoError(t, store.Revoke(ctx, minted.ID, identity))
	_, _, err = store.Verify(ctx, rawToken)
	assert.ErrorIs(t, err, ErrInvalidCredential)

	// idempotent revoke.
	require.NoError(t, store.Revoke(ctx, minted.ID, identity))

	creds, err := store.List(ctx, identity)
	require.NoError(t, err)
	require.Len(t, creds, 1)
	assert.Equal(t, identity, creds[0].Identity)
	assert.NotNil(t, creds[0].RevokedAt)
}

func TestASSShaped_WithoutExplicitCast_StillWorks(t *testing.T) {
	// Documents the "resolved" answer in README.md: IdentityCast is
	// optional even against a genuine uuid column, because pgx v5 infers
	// the type from query context. This test omits IdentityCast entirely.
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: assShapedCredentialSchema})

	var personID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, "INSERT INTO person DEFAULT VALUES RETURNING id").Scan(&personID))

	store, err := NewCredentialStore(ctx, StoreConfig{
		Pool:           db.Pool,
		IdentityColumn: "person_id",
		// No IdentityCast.
	})
	require.NoError(t, err)

	identity := personID.String()
	rawToken, _, err := store.Mint(ctx, identity)
	require.NoError(t, err)

	gotIdentity, _, err := store.Verify(ctx, rawToken)
	require.NoError(t, err)
	assert.Equal(t, identity, gotIdentity)
}
