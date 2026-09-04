package migrate

import (
	"database/sql"
	"embed"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// Source is a shared library's own embedded migration directory, applied
// via WithSource alongside (not merged into) a domain's own migrations. FS
// is typically the library's `//go:embed migrations/*.sql` var; Dir is the
// subdirectory within it; Name identifies the source and must be unique
// among a binary's WithSource calls -- it becomes part of the dedicated
// migrations-tracking table this source's versions live in.
type Source struct {
	Name string
	FS   embed.FS
	Dir  string
}

// ApplySource brings a single Source fully up to date against its own
// "schema_migrations_<name>" table, independent of the domain's own
// migrations table.
//
// This independence is not just tidiness -- it avoids a real correctness
// bug. golang-migrate tracks exactly one "current version" integer per
// migrations table, and Up() only ever looks for the next version strictly
// greater than that integer (source.Driver.Next's contract); it can never
// go back for a lower one. If a Source's migrations shared the domain's own
// table -- even numbered into a reserved-high range specifically to avoid
// colliding with the domain's own numbers -- applying one would jump the
// domain's tracked version past that range. Any domain migration added
// later with an ordinary, lower number would then be permanently invisible
// to Up(): not an error, just silently never applied, forever. A dedicated
// table per Source means the domain's own migrations keep incrementing from
// wherever they already are, in their own table, forever unaffected by
// what any Source does in its own.
func ApplySource(db *sql.DB, src Source) error {
	sourceDriver, err := iofs.New(src.FS, src.Dir)
	if err != nil {
		return fmt.Errorf("%s: failed to create migration source: %w", src.Name, err)
	}
	defer sourceDriver.Close()

	dbDriver, err := postgres.WithInstance(db, &postgres.Config{
		MigrationsTable: "schema_migrations_" + src.Name,
	})
	if err != nil {
		return fmt.Errorf("%s: failed to create database driver: %w", src.Name, err)
	}

	m, err := migrate.NewWithInstance("iofs", sourceDriver, "postgres", dbDriver)
	if err != nil {
		return fmt.Errorf("%s: failed to create migrator: %w", src.Name, err)
	}
	// Don't call m.Close() -- WithInstance doesn't own db, but Close() closes
	// it anyway (see Runner.createMigrator's identical comment).

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("%s: failed to run migrations: %w", src.Name, err)
	}
	return nil
}
