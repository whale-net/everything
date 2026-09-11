// Command migrate applies krill's schema (krill/migrate/schema/migrations,
// starting with 001_scope -- see issue #2487) via //libs/go/migrate's
// standard CLI, then seeds the one `scope` row this milestone creates with
// this repo's forge coordinates (LB1, NFR2). Migrations are embedded in
// the sibling schema package (not directly in this package) so anything
// else that needs the real schema -- e.g. a future store's Postgres
// integration tests -- applies the exact same SQL, mirroring
// whagent_net/migrate/main.go and audience_score_system/migrate/main.go.
package main

import (
	"github.com/whale-net/everything/krill/migrate/schema"
	"github.com/whale-net/everything/krill/migrate/seed"
	"github.com/whale-net/everything/libs/go/migrate"
)

func main() {
	migrate.RunCLI(schema.Migrations, schema.Dir,
		migrate.WithSeeder(seed.Seeder()),
	)
}
