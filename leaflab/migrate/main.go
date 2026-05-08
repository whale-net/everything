package main

import (
	"embed"

	"github.com/whale-net/everything/firmware/sensor/catalog"
	"github.com/whale-net/everything/libs/go/migrate"
)

//go:embed migrations/*.sql
var migrations embed.FS

func main() {
	migrate.RunCLI(migrations, "migrations",
		migrate.WithSeeder(catalog.Seeder()),
	)
}
