package main

import (
	"github.com/whale-net/everything/firmware/sensor/catalog"
	"github.com/whale-net/everything/leaflab/migrate/schema"
	"github.com/whale-net/everything/libs/go/migrate"
)

func main() {
	migrate.RunCLI(schema.Migrations, schema.Dir,
		migrate.WithSeeder(catalog.Seeder()),
	)
}
