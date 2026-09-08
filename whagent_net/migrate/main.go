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
package main

import (
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/whagent_net/migrate/schema"
)

func main() {
	migrate.RunCLI(schema.Migrations, schema.Dir)
}
