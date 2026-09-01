package main

import (
	"github.com/whale-net/everything/audience_score_system/migrate/schema"
	"github.com/whale-net/everything/libs/go/migrate"
)

func main() {
	migrate.RunCLI(schema.Migrations, schema.Dir)
}
