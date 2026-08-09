// Package schema embeds the App Registry's golang-migrate SQL migrations and
// exposes them as an importable embed.FS. It exists so the migration
// runner (migrate/main.go, package main) and anything else that needs the
// real schema — notably the Postgres integration tests in
// server/repository/postgres — apply the exact same SQL rather than each
// maintaining its own copy.
package schema

import "embed"

// Migrations is the embedded set of golang-migrate SQL files.
//
//go:embed migrations/*.sql
var Migrations embed.FS

// Dir is the subdirectory within Migrations holding the migration files,
// for use with libs/go/migrate.NewRunner / RunCLI.
const Dir = "migrations"
