//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See //libs/go/dbtest's README and
// whagent_net/migrate/seed/seed_integration_test.go for the pattern this
// file follows: spin up a throwaway Postgres via dbtest, apply krill's own
// real embedded migrations, then exercise SeedScope (seed.go) against it --
// issue #2487's Testing section: "seeding twice yields exactly one scope
// row with the expected forge coordinates".
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/migrate/seed:seed_integration_test --test_output=all
package seed_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/migrate/schema"
	"github.com/whale-net/everything/krill/migrate/seed"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// newTestDB provisions an isolated, migrated Postgres database (mirrors
// whagent_net/migrate/seed/seed_integration_test.go's newTestDB).
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply migration 001 from the real embedded schema")

	return sqlDB
}

type scopeRow struct {
	RepoFullName       string
	DefaultBranch      string
	PointerIssueNumber *int
}

func readScopeRows(t *testing.T, ctx context.Context, db *sql.DB) []scopeRow {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT repo_full_name, default_branch, pointer_issue_number FROM scope ORDER BY repo_full_name
	`)
	require.NoError(t, err)
	defer rows.Close()

	var out []scopeRow
	for rows.Next() {
		var r scopeRow
		require.NoError(t, rows.Scan(&r.RepoFullName, &r.DefaultBranch, &r.PointerIssueNumber))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

// TestSeedScope_CreatesExpectedRow proves seeding an empty database creates
// exactly one scope row carrying this repo's forge coordinates
// (repo_full_name "whale-net/everything", default branch "main"), with
// pointer_issue_number left unset (NFR2/FR20).
func TestSeedScope_CreatesExpectedRow(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	require.NoError(t, seed.SeedScope(ctx, db))

	rows := readScopeRows(t, ctx, db)
	require.Len(t, rows, 1)
	assert.Equal(t, "whale-net/everything", rows[0].RepoFullName)
	assert.Equal(t, "main", rows[0].DefaultBranch)
	assert.Nil(t, rows[0].PointerIssueNumber, "pointer_issue_number must be left unset at seed time")
}

// TestSeedScope_ReRunIsIdempotent proves re-running SeedScope produces zero
// new rows -- issue #2487's Testing section: "seeding twice yields exactly
// one scope row with the expected forge coordinates".
func TestSeedScope_ReRunIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	require.NoError(t, seed.SeedScope(ctx, db))
	require.NoError(t, seed.SeedScope(ctx, db))
	require.NoError(t, seed.SeedScope(ctx, db))

	rows := readScopeRows(t, ctx, db)
	require.Len(t, rows, 1, "re-running the seeder must not mint a second scope row")
	assert.Equal(t, "whale-net/everything", rows[0].RepoFullName)
	assert.Equal(t, "main", rows[0].DefaultBranch)
}

// TestSeedScope_DoesNotClobberOperatorEdit proves a re-run leaves an
// operator's subsequent edit to the existing row (e.g. pointer_issue_number
// populated by a later FR20 task, or a hand-corrected default_branch)
// untouched -- SeedScope's ON CONFLICT DO NOTHING must never overwrite a
// row already there.
func TestSeedScope_DoesNotClobberOperatorEdit(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	require.NoError(t, seed.SeedScope(ctx, db))

	_, err := db.ExecContext(ctx, `
		UPDATE scope SET pointer_issue_number = 42, default_branch = 'trunk' WHERE repo_full_name = $1
	`, "whale-net/everything")
	require.NoError(t, err)

	require.NoError(t, seed.SeedScope(ctx, db))

	rows := readScopeRows(t, ctx, db)
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].PointerIssueNumber)
	assert.Equal(t, 42, *rows[0].PointerIssueNumber, "a re-run must not clobber an operator's later pointer_issue_number edit")
	assert.Equal(t, "trunk", rows[0].DefaultBranch, "a re-run must not re-assert default_branch over an operator's hand correction")
}

// readCredentialRows returns every mcp_credential row, ordered by identity.
func readCredentialRows(t *testing.T, ctx context.Context, db *sql.DB) []CredentialRow {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT identity, token_hash FROM mcp_credential ORDER BY identity
	`)
	require.NoError(t, err)
	defer rows.Close()

	var out []CredentialRow
	for rows.Next() {
		var c CredentialRow
		require.NoError(t, rows.Scan(&c.Identity, &c.TokenHash))
		out = append(out, c)
	}
	require.NoError(t, rows.Err())
	return out
}

type CredentialRow struct {
	Identity  string
	TokenHash string
}

// TestSeedDevCredential_StoresTheHashTheStoreVerifies is the test that
// matters for the plugin mounts: the committed krill-work/krill-design
// configs present `Authorization: Bearer dev-local`, and
// auth.CredentialStore.Verify resolves a token by looking up
// sha256(rawToken) in token_hash. This asserts the seeded hash is
// byte-identical to that value, so the literal in the committed config
// actually verifies.
func TestSeedDevCredential_StoresTheHashTheStoreVerifies(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	require.NoError(t, seed.SeedDevCredential(ctx, db))

	rows := readCredentialRows(t, ctx, db)
	require.Len(t, rows, 1)
	assert.Equal(t, "dev-local", rows[0].Identity)
	// sha256("dev-local"), hex-encoded -- the exact form
	// libs/go/auth.hashToken produces and Verify compares against.
	assert.Equal(t, "32f6050ffbc1d8c0b7d607d81dd2c14b6fdab2d2bb1ff345e21fcc3f76c008ce", rows[0].TokenHash)

	// Only ever the hash: the table must never hold the raw token.
	assert.NotContains(t, rows[0].TokenHash, "dev-local", "the raw token must never be persisted")
}

// TestSeedDevCredential_IsIdempotent proves re-running the seeder neither
// duplicates the row nor errors, matching the scope seeder's contract --
// `migrate` runs its seeder on every invocation.
func TestSeedDevCredential_IsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	require.NoError(t, seed.SeedDevCredential(ctx, db))
	require.NoError(t, seed.SeedDevCredential(ctx, db))
	require.NoError(t, seed.SeedDevCredential(ctx, db))

	rows := readCredentialRows(t, ctx, db)
	assert.Len(t, rows, 1, "three seed runs must still leave exactly one credential")
}

// TestSeedDevCredential_DoesNotRevokeAnOperatorRotation proves the seeder
// never clobbers a credential an operator replaced. If they minted their
// own row for the same identity, the UNIQUE token_hash conflict means the
// seed is a no-op rather than an overwrite -- so the old token keeps
// verifying and no one is locked out.
func TestSeedDevCredential_DoesNotRevokeAnOperatorRotation(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	// An operator already holds a credential for this identity, with a
	// different token.
	other := sha256.Sum256([]byte("an-operators-own-token"))
	_, err := db.ExecContext(ctx, `
		INSERT INTO mcp_credential (identity, token_hash) VALUES ($1, $2)
	`, "dev-local", hex.EncodeToString(other[:]))
	require.NoError(t, err)

	require.NoError(t, seed.SeedDevCredential(ctx, db))

	rows := readCredentialRows(t, ctx, db)
	assert.Len(t, rows, 2, "the operator's own credential must survive the seed")
	var found bool
	for _, c := range rows {
		if c.TokenHash == hex.EncodeToString(other[:]) {
			found = true
		}
	}
	assert.True(t, found, "the operator's token_hash must not be overwritten or revoked")
}
