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
