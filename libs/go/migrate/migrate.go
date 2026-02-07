package migrate

import (
	"database/sql"
	"embed"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// Runner provides database migration functionality
type Runner struct {
	db         *sql.DB
	migrations embed.FS
	migrateDir string
}

// NewRunner creates a new migration runner
// migrateDir is the subdirectory within the embedded FS (e.g., "migrations")
func NewRunner(db *sql.DB, migrations embed.FS, migrateDir string) *Runner {
	return &Runner{
		db:         db,
		migrations: migrations,
		migrateDir: migrateDir,
	}
}

// Up runs all pending migrations
func (r *Runner) Up() error {
	m, err := r.createMigrator()
	if err != nil {
		return err
	}
	// Don't defer m.Close() here - we're using WithInstance which doesn't own the DB

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	return nil
}

// Down rolls back all migrations
func (r *Runner) Down() error {
	m, err := r.createMigrator()
	if err != nil {
		return err
	}
	// Don't defer m.Close() here - we're using WithInstance which doesn't own the DB

	if err := m.Down(); err != nil {
		return fmt.Errorf("failed to rollback all migrations: %w", err)
	}

	return nil
}

// Steps runs n migrations (positive = up, negative = down)
func (r *Runner) Steps(n int) error {
	m, err := r.createMigrator()
	if err != nil {
		return err
	}
	// Don't defer m.Close() here - we're using WithInstance which doesn't own the DB

	if err := m.Steps(n); err != nil {
		return fmt.Errorf("failed to run %d steps: %w", n, err)
	}

	return nil
}

// Version returns the current migration version and dirty state
func (r *Runner) Version() (version uint, dirty bool, err error) {
	m, err := r.createMigrator()
	if err != nil {
		return 0, false, err
	}
	// Don't defer m.Close() here - we're using WithInstance which doesn't own the DB

	version, dirty, err = m.Version()
	if err != nil && err != migrate.ErrNilVersion {
		return 0, false, fmt.Errorf("failed to get version: %w", err)
	}

	return version, dirty, nil
}

// Force sets the migration version without running migrations
// Useful for recovering from a dirty state
func (r *Runner) Force(version int) error {
	m, err := r.createMigrator()
	if err != nil {
		return err
	}
	// Don't defer m.Close() here - we're using WithInstance which doesn't own the DB

	if err := m.Force(version); err != nil {
		return fmt.Errorf("failed to force version %d: %w", version, err)
	}

	return nil
}

func (r *Runner) createMigrator() (*migrate.Migrate, error) {
	// Create source driver from embedded files
	sourceDriver, err := iofs.New(r.migrations, r.migrateDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create migration source: %w", err)
	}

	// Create database driver
	dbDriver, err := postgres.WithInstance(r.db, &postgres.Config{})
	if err != nil {
		return nil, fmt.Errorf("failed to create database driver: %w", err)
	}

	// Create migrator
	m, err := migrate.NewWithInstance(
		"iofs",
		sourceDriver,
		"postgres",
		dbDriver,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create migrator: %w", err)
	}

	return m, nil
}
