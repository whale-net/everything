// Package schema embeds Audience Score System's golang-migrate SQL
// migrations and exposes them as an importable embed.FS. It exists so the
// migration runner (migrate/main.go, package main) and anything else that
// needs the real schema — notably Postgres integration tests, once they
// exist — apply the exact same SQL rather than each maintaining its own
// copy. Modelled on tools/app_registry/migrate/schema/schema.go.
//
// Migration 001 (identity: person, channel, channel_person, channel_invite
// — see #1568) is the first real migration; more land as later M1 schema
// tasks add tables. The directive below uses the "all:" embed prefix
// rather than a "*.sql" glob because it was originally written when the
// migrations/ directory was empty and go:embed errors at compile time on a
// glob matching nothing; kept as-is now that real files exist since it's
// still correct and avoids narrowing the embed to *.sql only.
package schema

import "embed"

// Migrations is the embedded set of golang-migrate SQL files.
//
//go:embed all:migrations
var Migrations embed.FS

// Dir is the subdirectory within Migrations holding the migration files,
// for use with libs/go/migrate.NewRunner / RunCLI.
const Dir = "migrations"
