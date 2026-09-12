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
package seed

import (
	"context"
	"database/sql"
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
)

// Seeder returns a libs/go/migrate.Seeder-compatible function that ensures
// krill's one `scope` row exists, per this package's doc comment.
//
// Compatible with libs/go/migrate.WithSeeder -- pass the result directly:
//
//	migrate.RunCLI(schema.Migrations, schema.Dir, migrate.WithSeeder(seed.Seeder()))
func Seeder() func(ctx context.Context, db *sql.DB) error {
	return func(ctx context.Context, db *sql.DB) error {
		return SeedScope(ctx, db)
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
