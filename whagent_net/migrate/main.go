// Command migrate applies whagent-net's `session` store schema
// (whagent_net/migrate/migrations, starting with 001_initial_schema --
// see issue #2109) via //libs/go/migrate's standard CLI. Modelled on
// manmanv2/migrate/main.go: migrations are embedded directly in this
// package rather than a separate schema sub-package, since no other
// package here needs to import the embedded FS (contrast
// audience_score_system/migrate, whose migrate/schema package is also
// imported by that domain's store integration tests).
package main

import (
	"embed"

	"github.com/whale-net/everything/libs/go/migrate"
)

//go:embed migrations/*.sql
var migrations embed.FS

func main() {
	migrate.RunCLI(migrations, "migrations")
}
