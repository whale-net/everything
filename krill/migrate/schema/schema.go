// Package schema embeds krill's golang-migrate SQL migrations and exposes
// them as an importable embed.FS. It exists so the migration runner
// (migrate/main.go, package main) and anything else that needs the real
// schema -- e.g. a future store's Postgres integration tests -- apply the
// exact same SQL rather than each maintaining its own copy. Modelled on
// whagent_net/migrate/schema/schema.go and
// audience_score_system/migrate/schema/schema.go.
//
// Migration numbering for M1 is assigned up front (issue #2487) so
// parallel tasks never collide: 001 scope (this task), 002 spec entities,
// 003 session, 004 milestone reference + association, 005 pointer
// artifact.
package schema

import "embed"

// Migrations is the embedded set of golang-migrate SQL files. "all:" (not
// a "*.sql" glob) is used defensively: go:embed errors at compile time on
// a glob matching nothing, and this package must still compile between
// the moment its BUILD.bazel target is scaffolded and the moment the
// first migration file lands.
//
//go:embed all:migrations
var Migrations embed.FS

// Dir is the subdirectory within Migrations holding the migration files,
// for use with libs/go/migrate.NewRunner / RunCLI.
const Dir = "migrations"
