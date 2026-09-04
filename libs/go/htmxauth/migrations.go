package htmxauth

import "embed"

// Migrations is the schema this package's DBSessionManager (ui_sessions)
// depends on. A domain adopting DB-backed sessions wires it into its own
// migrate/main.go with
// migrate.WithSource("htmxauth", htmxauth.Migrations, "migrations") instead
// of copying the SQL into its own migrations directory — see README.md
// "DB-Backed Sessions". WithSource tracks this in its own
// "schema_migrations_htmxauth" table, independent of the domain's own
// migrations table (see libs/go/migrate's ApplySource for why that
// independence matters) — so it numbers from 1 like any ordinary migration
// sequence, not from a reserved high range.
//
//go:embed migrations/*.sql
var Migrations embed.FS
