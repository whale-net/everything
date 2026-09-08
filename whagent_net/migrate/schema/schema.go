// Package schema embeds whagent-net's golang-migrate SQL migrations and
// exposes them as an importable embed.FS. It exists so the migration
// runner (migrate/main.go, package main) and anything else that needs the
// real schema -- notably the `whagent_net/session` store's Postgres
// integration tests (#2109) -- apply the exact same SQL rather than each
// maintaining its own copy. Modelled on
// audience_score_system/migrate/schema/schema.go.
package schema

import "embed"

// Migrations is the embedded set of golang-migrate SQL files.
//
//go:embed all:migrations
var Migrations embed.FS

// Dir is the subdirectory within Migrations holding the migration files,
// for use with libs/go/migrate.NewRunner / RunCLI.
const Dir = "migrations"
