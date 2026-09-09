// Command migrate applies whagent-net's `session` store schema
// (whagent_net/migrate/schema/migrations, starting with 001_initial_schema
// -- see issue #2109) via //libs/go/migrate's standard CLI. Migrations are
// embedded in the sibling schema package (not directly in this package)
// because the `whagent_net/session` store's Postgres integration tests
// (#2109 Testing) need to apply the exact same schema -- same shape as
// audience_score_system/migrate, whose migrate/schema package is imported
// by that domain's store integration tests. (Scaffold's original comment
// here assumed no other package would need the embedded FS; that
// assumption didn't survive Testing.)
//
// migrate.WithSeeder(seed.Seeder()) (issue #2121) runs the
// agent-definition seeder as a post-migration step, after every
// successful `up` -- see whagent_net/migrate/seed's package doc comment
// for the seeding/versioning contract, and whagent_net/config for the
// checked-in agents.yaml it seeds from. Mirrors leaflab/migrate/main.go's
// identical WithSeeder(catalog.Seeder()) shape.
package main

import (
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/whagent_net/migrate/schema"
	"github.com/whale-net/everything/whagent_net/migrate/seed"
)

func main() {
	migrate.RunCLI(schema.Migrations, schema.Dir,
		migrate.WithSeeder(seed.Seeder()),
	)
}
