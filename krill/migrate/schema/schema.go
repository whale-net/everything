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
//
// Every later milestone reserves its own migration number(s) up front the
// same way, for the identical reason -- see each milestone's own
// ARCHITECTURE.md numbering note for the full per-migration rationale:
// 006-007 (still M1, the mcpauth auth-flow gap,
// ARCHITECTURE/04-migration-numbering-m1.md), 008-009 (M2,
// ARCHITECTURE/05-migration-numbering-m2.md), 010-014 (M3,
// ARCHITECTURE/06-migration-numbering-m3.md), 015 (M4's whole work axis
// in one migration, ARCHITECTURE/28-work-axis-m4.md), and 016 (M5's
// whole escalation/intervention/console axis in one migration,
// ARCHITECTURE/31-escalation-console-m5.md).
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
