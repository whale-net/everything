package htmxauth

import "embed"

// Migrations is the schema this package's DBSessionManager (ui_sessions)
// depends on. A domain adopting DB-backed sessions wires it into its own
// migrate/main.go with migrate.WithSource(htmxauth.Migrations, "migrations")
// instead of copying the SQL into its own migrations directory — see
// README.md "DB-Backed Sessions". Numbered starting at 900001 to stay above
// libs/go/migrate's reserved floor for shared-library migrations, so it
// never collides with a domain's own sequential migration numbers.
//
//go:embed migrations/*.sql
var Migrations embed.FS
