// Package seed is krill's scope seeder (issue #2487, LB1/NFR2): a
// libs/go/migrate.Seeder that ensures exactly one `scope` row exists,
// carrying this repo's forge coordinates. NFR2 requires the seeder to do
// this explicitly rather than leaving it to template inference -- this
// package is that explicit step, run by krill/migrate/main.go on every
// `migrate` invocation (migrate.WithSeeder(seed.Seeder())).
//
// # Idempotency
//
// repoFullName is the seeded row's natural key (`scope.repo_full_name` is
// UNIQUE, migrations/001_scope.up.sql). SeedScope inserts the one row on
// first run and is a no-op on every later run -- it never clobbers a row
// already there (e.g. one whose pointer_issue_number a later FR20 task has
// since populated), matching this package's "not left to inference" remit
// without re-asserting defaultBranch over a value an operator may have
// since corrected by hand.
//
// It also seeds the local-development MCP credential (`SeedDevCredential`),
// for the reason documented on that function: the committed plugin configs
// present a fixed `Bearer dev-local` that nothing else provisions.
package seed

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"strings"
)

// repoFullName and defaultBranch are krill's own forge coordinates
// (LB1) -- this milestone runs against exactly one repo/scope, so these
// are seeded explicitly rather than read from the environment (NFR2:
// "the seeder to do this explicitly rather than leaving it to template
// inference"). pointerIssueNumber is left unset: FR20's thin GitHub
// pointer issue does not exist until a later M1 task creates one and
// records it here.
const (
	repoFullName  = "whale-net/everything"
	defaultBranch = "main"

	// devCredentialIdentity and devCredentialToken are the local-development
	// MCP credential the committed plugin configs present as
	// `Authorization: Bearer dev-local` (krill/plugin/{work,design}/
	// mcp_config.json). Nothing else provisions one, so without this row
	// every /mcp/spec, /mcp/work and /mcp/design call 401s with "auth:
	// invalid or revoked credential" against a freshly migrated database.
	//
	// SECURITY: this token is committed in a PUBLIC repository, so it is
	// readable by anyone. It is only ever usable when
	// allowDevCredential is true, which requires an operator to opt in
	// explicitly (see below) -- never by default, and never in a deployed
	// environment.
	devCredentialIdentity = "dev-local"
	devCredentialToken    = "dev-local"
)

// AllowDevCredential reports whether the dev-credential opt-in is set.
//
// `migrate` runs on every deployment, prod included, so seeding an
// unconditionally-known token would be an authentication bypass on the MCP
// surface in any environment that has not thought to remove it. The gate
// reads KRILL_ALLOW_DEV_CREDENTIAL, which the Tiltfile sets for the local
// cluster and no deployment sets -- so the failure mode is "the local dev
// loop needs one line of config", never "prod accepts a public token".
//
// The value is read from the process environment rather than inferred from
// anything about the database, because nothing about a connection string
// reliably distinguishes dev from prod.
func AllowDevCredential() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("KRILL_ALLOW_DEV_CREDENTIAL")), "true")
}

// Seeder returns a libs/go/migrate.Seeder-compatible function that ensures
// krill's one `scope` row exists, per this package's doc comment.
//
// Compatible with libs/go/migrate.WithSeeder -- pass the result directly:
//
//	migrate.RunCLI(schema.Migrations, schema.Dir, migrate.WithSeeder(seed.Seeder()))
func Seeder() func(ctx context.Context, db *sql.DB) error {
	return func(ctx context.Context, db *sql.DB) error {
		if err := SeedScope(ctx, db); err != nil {
			return err
		}
		if !AllowDevCredential() {
			return nil
		}
		return SeedDevCredential(ctx, db)
	}
}

// SeedScope is Seeder's implementation, factored out so this task's
// Testing phase can exercise it directly against a real Postgres instance
// without going through the full migrate.RunCLI path.
func SeedScope(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO scope (repo_full_name, default_branch)
		VALUES ($1, $2)
		ON CONFLICT (repo_full_name) DO NOTHING
	`, repoFullName, defaultBranch)
	return err
}

// SeedDevCredential inserts the local-development MCP credential the
// committed plugin configs present as `Bearer dev-local`.
//
// It stores only the SHA-256 hash, byte-identical to
// `auth.hashToken` (libs/go/auth/credential.go) — the same value Verify
// looks up, and the only form the table ever holds. There is no
// credential row without this, and the plugin configs have shipped that
// literal since M1, so a database that has never been seeded here 401s
// every MCP call with the opaque "invalid or revoked credential" that an
// unknown token and a revoked one are deliberately indistinguishable
// between (NFR1).
//
// Idempotent via the UNIQUE token_hash constraint: a later run is a
// no-op, and it never clobbers or revokes a credential an operator
// rotated. This is a development convenience only — the token is
// committed in a public repository, so it must never be reachable in a
// deployed environment. Seeder calls it ONLY when allowDevCredential() is
// true, so `migrate` run without KRILL_ALLOW_DEV_CREDENTIAL never creates
// it. Call this function directly only in a context that has established
// the same thing.
func SeedDevCredential(ctx context.Context, db *sql.DB) error {
	sum := sha256.Sum256([]byte(devCredentialToken))
	_, err := db.ExecContext(ctx, `
		INSERT INTO mcp_credential (identity, token_hash)
		VALUES ($1, $2)
		ON CONFLICT (token_hash) DO NOTHING
	`, devCredentialIdentity, hex.EncodeToString(sum[:]))
	return err
}
