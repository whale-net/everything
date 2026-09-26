package main

import (
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/manmanv2/migrate/schema"
)

func main() {
	migrate.RunCLI(schema.Migrations, schema.Dir)
}
