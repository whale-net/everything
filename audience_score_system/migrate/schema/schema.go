// Package schema embeds Audience Score System's golang-migrate SQL
// migrations and exposes them as an importable embed.FS. It exists so the
// migration runner (migrate/main.go, package main) and anything else that
// needs the real schema — notably Postgres integration tests, once they
// exist — apply the exact same SQL rather than each maintaining its own
// copy. Modelled on tools/app_registry/migrate/schema/schema.go.
//
// No migrations exist yet (added by later M1 schema tasks); Migrations
// currently embeds an empty migrations/ directory (holding only a
// .gitkeep placeholder). The directive below uses the "all:" embed prefix
// rather than a "*.sql" glob specifically so the package still compiles
// with zero .sql files present — go:embed errors at compile time on a glob
// that matches nothing. golang-migrate's iofs source (see
// libs/go/migrate.Runner) ignores non-migration filenames like .gitkeep,
// so this is safe once real *.sql files are added too.
package schema

import "embed"

// Migrations is the embedded set of golang-migrate SQL files (plus a
// .gitkeep placeholder until the first migration lands).
//
//go:embed all:migrations
var Migrations embed.FS

// Dir is the subdirectory within Migrations holding the migration files,
// for use with libs/go/migrate.NewRunner / RunCLI.
const Dir = "migrations"
