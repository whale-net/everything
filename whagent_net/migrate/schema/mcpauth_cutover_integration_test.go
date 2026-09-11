//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See the go_test target's gotags in BUILD.bazel for how to run it.
//
// Testing-phase coverage for issue #2434 (FR11, NFR8) beyond what
// migration 009_mcpauth_cutover's own Implementation-phase commit already
// covers (the up/down.sql files and their comments): this proves the
// migration actually behaves as designed against a real Postgres instance
// seeded with pre-cutover rows, and that the resulting empty table really
// does reject a pre-cutover credential end to end through
// whagent_net/mcp/server's verifier -- not just "the DELETE statement
// looks right by inspection".
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //whagent_net/migrate/schema:schema_integration_test --test_output=all
package schema_test

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/mcpauth"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/whagent_net/migrate/schema"

	mcpserver "github.com/whale-net/everything/whagent_net/mcp/server"
)

// preCutoverDB stands up a real Postgres database and steps the schema up
// to version 8 (008_delegated_grant) ONLY -- i.e. everything migration
// 009_mcpauth_cutover is about to delete, deliberately stopped one version
// short of the cutover itself, so this file's tests can seed pre-cutover
// rows before applying it.
func preCutoverDB(t *testing.T) (ctx context.Context, db *dbtest.Postgres, runner *migrate.Runner) {
	t.Helper()
	ctx = context.Background()
	db = dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner = migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Migrate(8), "step up to 008_delegated_grant only -- one version short of the 009 cutover")

	version, dirty, err := runner.Version()
	require.NoError(t, err)
	require.False(t, dirty)
	require.Equal(t, uint(8), version)

	return ctx, db, runner
}

func countRows(t *testing.T, ctx context.Context, db *dbtest.Postgres, table string) int {
	t.Helper()
	var n int
	require.NoError(t, db.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n))
	return n
}

// TestMigration009_DeletesAllPreCutoverCredentialAndAuthCodeRows is this
// issue's first Testing-section bullet: "Migration applied to a database
// seeded with pre-cutover mcp_credential rows leaves zero rows." It seeds
// both a live and an already-revoked mcp_credential row (FR11 targets
// every pre-cutover credential regardless of revocation state -- there is
// nothing left to distinguish), plus a pending mcp_auth_code row (an
// in-flight, not-yet-exchanged authorization that must not be allowed to
// complete post-cutover), and a mcp_oauth_client row (an RFC 7591 client
// registration, which this issue's Implementation-phase decision --
// documented in 009_mcpauth_cutover.up.sql's own comment -- leaves
// untouched since it carries no operator's impersonation-backed
// authority).
func TestMigration009_DeletesAllPreCutoverCredentialAndAuthCodeRows(t *testing.T) {
	ctx, db, runner := preCutoverDB(t)

	_, err := db.Pool.Exec(ctx, `
		INSERT INTO mcp_credential (identity, token_hash) VALUES
			('pre-cutover-identity-1', 'hash-of-a-live-pre-cutover-credential'),
			('pre-cutover-identity-2', 'hash-of-an-already-revoked-credential')
	`)
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `
		UPDATE mcp_credential SET revoked_at = NOW() WHERE identity = 'pre-cutover-identity-2'
	`)
	require.NoError(t, err)

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO mcp_auth_code (code_hash, client_id, redirect_uri, identity, code_challenge, code_challenge_method, expires_at)
		VALUES ('pending-auth-code-hash', 'some-client-id', 'https://client.example/callback', 'pre-cutover-identity-1', 'challenge', 'S256', NOW() + interval '1 minute')
	`)
	require.NoError(t, err)

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO mcp_oauth_client (client_id, metadata) VALUES ('some-client-id', '{"client_name": "test client"}'::jsonb)
	`)
	require.NoError(t, err)

	require.Equal(t, 2, countRows(t, ctx, db, "mcp_credential"), "seed sanity check")
	require.Equal(t, 1, countRows(t, ctx, db, "mcp_auth_code"), "seed sanity check")
	require.Equal(t, 1, countRows(t, ctx, db, "mcp_oauth_client"), "seed sanity check")

	require.NoError(t, runner.Migrate(9), "apply 009_mcpauth_cutover")

	assert.Equal(t, 0, countRows(t, ctx, db, "mcp_credential"), "every pre-cutover mcp_credential row (live or already-revoked) must be gone after the cutover migration (FR11)")
	assert.Equal(t, 0, countRows(t, ctx, db, "mcp_auth_code"), "every pending pre-cutover mcp_auth_code row must be gone -- an in-flight authorization must not be able to complete post-cutover")
	assert.Equal(t, 1, countRows(t, ctx, db, "mcp_oauth_client"), "mcp_oauth_client rows are RFC 7591 client registrations, not credentials -- the cutover must leave them alone")
}

// TestMigration009_PreCutoverCredentialRejectedAfterCutover_NoFallback is
// this issue's second Testing-section bullet: "A request presenting a
// pre-cutover credential is rejected -- it does not resolve to an identity
// that can acquire a token, and it does not fall back to the manual-token
// path." This drives the real libs/go/mcpauth.CredentialStore AND the
// real whagent_net/mcp/server.NewVerifier against the same
// migration-applied database, end to end: mint a credential before
// cutover, apply 009, then present that exact raw token to the real
// verifier mcp wires into every request.
func TestMigration009_PreCutoverCredentialRejectedAfterCutover_NoFallback(t *testing.T) {
	ctx, db, runner := preCutoverDB(t)

	credentialsBeforeCutover, err := mcpauth.NewCredentialStore(ctx, mcpauth.StoreConfig{Pool: db.Pool})
	require.NoError(t, err)

	rawToken, cred, err := credentialsBeforeCutover.Mint(ctx, "pre-cutover-operator-identity")
	require.NoError(t, err)
	require.NotEmpty(t, rawToken)
	require.Nil(t, cred.RevokedAt)

	require.NoError(t, runner.Migrate(9), "apply 009_mcpauth_cutover")

	// A fresh CredentialStore against the SAME (now-cutover) table --
	// mirrors how `mcp` and `ui` each construct their own store against
	// whatever mcp_credential currently holds; nothing here is a cached
	// reference to the pre-cutover row.
	credentialsAfterCutover, err := mcpauth.NewCredentialStore(ctx, mcpauth.StoreConfig{Pool: db.Pool})
	require.NoError(t, err)

	_, _, err = credentialsAfterCutover.Verify(ctx, rawToken)
	assert.ErrorIs(t, err, mcpauth.ErrInvalidCredential, "a credential minted before cutover must not verify after 009 has run")

	// Same assertion one layer up, through the real verifier
	// AuthMiddleware/NewHTTPHandler wire onto every mcp request
	// (whagent_net/mcp/server/auth.go) -- proves the rejection surfaces
	// exactly like any other invalid credential, and never falls back to
	// treating the presented token as a manual (pass-through) bearer
	// token: NewVerifier's credential-shaped branch either resolves an
	// identity or rejects outright, it never falls through to the
	// tokenExtraKey/manual-token branch below it.
	verifier := mcpserver.NewVerifier(credentialsAfterCutover)
	info, err := verifier(ctx, rawToken, nil)
	assert.Nil(t, info)
	assert.ErrorIs(t, err, sdkauth.ErrInvalidToken, "the pre-cutover credential must be rejected outright by mcp's own verifier, with no fallback to the manual-token path")
}
