package main

import (
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/tools/app_registry/migrate/schema"
)

func main() {
	migrate.RunCLI(schema.Migrations, schema.Dir)
}
