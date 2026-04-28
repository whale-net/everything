package migrate

import (
	"database/sql"
	"embed"
	"fmt"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// Runner provides database migration functionality
type Runner struct {
	db              *sql.DB
	migrations      embed.FS
	migrateDir      string
	tracker         *HistoryTracker
	repeatableDir   string
	repeatableStore RepeatableStore
}

// NewRunner creates a new migration runner
// migrateDir is the subdirectory within the embedded FS (e.g., "migrations")
func NewRunner(db *sql.DB, migrations embed.FS, migrateDir string) *Runner {
	return &Runner{
		db:         db,
		migrations: migrations,
		migrateDir: migrateDir,
		tracker:    NewHistoryTracker(db),
	}
}

// WithRepeatableMigrations configures the runner to also execute repeatable migrations
// from repeatableDir after all versioned migrations have been applied.
// Repeatable migrations are files named "R__<description>.sql" inside repeatableDir.
// A migration is only (re-)run when its content has changed since the last successful run.
// WithRepeatableMigrations returns the receiver to allow method chaining.
func (r *Runner) WithRepeatableMigrations(repeatableDir string) *Runner {
	r.repeatableDir = repeatableDir
	r.repeatableStore = NewRepeatableTracker(r.db)
	return r
}

// History returns a simplified repository interface for accessing migration history
func (r *Runner) History() *HistoryRepo {
	return NewHistoryRepo(r.tracker)
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

// ForceWithValidation forces a version after validating against migration history
func (r *Runner) ForceWithValidation(version int, dangerous bool) error {
	// Ensure history table exists
	if err := r.tracker.EnsureHistoryTable(); err != nil {
		return fmt.Errorf("failed to ensure history table: %w", err)
	}

	// Validate recovery unless dangerous flag is set
	if !dangerous {
		repo := r.History()
		safe, reason, err := repo.IsVersionSafe(int64(version))
		if err != nil {
			return fmt.Errorf("failed to validate version: %w", err)
		}
		if !safe {
			return fmt.Errorf("cannot force to version %d: %s\nUse --force-dangerous to override (not recommended)", version, reason)
		}
	}

	// Perform the force
	return r.Force(version)
}

// UpWithTracking runs migrations one at a time with history tracking
func (r *Runner) UpWithTracking() error {
	// Ensure history table exists first
	if err := r.tracker.EnsureHistoryTable(); err != nil {
		return fmt.Errorf("failed to ensure history table: %w", err)
	}

	// Get current version
	currentVersion, dirty, err := r.Version()
	if err != nil && err.Error() != "no migration" {
		return fmt.Errorf("failed to get current version: %w", err)
	}

	if dirty {
		return fmt.Errorf("database is in dirty state (version %d). Use --force to recover", currentVersion)
	}

	// Create ONE migrator for all steps. Creating a new migrator per iteration leaks
	// the dedicated advisory-lock connection acquired by postgres.WithInstance, exhausting
	// the connection pool after ~MaxOpenConns/2 migrations.
	// Do NOT call m.Close() — WithInstance doesn't own the DB, but Close() closes it anyway,
	// which would break callers that use the DB after UpWithTracking returns.
	// The advisory lock is acquired and released by Lock()/Unlock() inside each Steps() call.
	m, err := r.createMigrator()
	if err != nil {
		return err
	}

	// Track version locally — avoids calling r.Version() (which creates another migrator)
	// on every iteration.
	nextVersion := currentVersion + 1

	for {
		startTime := time.Now()

		// Try to run one migration
		stepErr := m.Steps(1)

		if stepErr == migrate.ErrNoChange {
			// No more migrations - this is success
			return nil
		}

		if stepErr != nil {
			// Check if this is a "file doesn't exist" error (no more migrations)
			if stepErr.Error() == "file does not exist" {
				// No more migration files - this is normal
				return nil
			}

			// Real migration error - record it
			historyID, recErr := r.tracker.RecordStart(int64(nextVersion), "up")
			if recErr != nil {
				fmt.Printf("Warning: failed to record migration start in history: %v\n", recErr)
			}

			fmt.Printf("✗ Migration %d failed: %v\n", nextVersion, stepErr)
			if historyID > 0 {
				if recErr := r.tracker.RecordFailure(historyID, startTime, stepErr); recErr != nil {
					fmt.Printf("Warning: failed to record migration failure in history: %v\n", recErr)
				}
			}
			return stepErr
		}

		// Migration succeeded - record it in history
		historyID, err := r.tracker.RecordStart(int64(nextVersion), "up")
		if err != nil {
			fmt.Printf("Warning: failed to record migration start in history: %v\n", err)
		}

		fmt.Printf("✓ Migration %d completed successfully (%dms)\n", nextVersion, time.Since(startTime).Milliseconds())
		if historyID > 0 {
			if err := r.tracker.RecordSuccess(historyID, startTime); err != nil {
				fmt.Printf("Warning: failed to record migration success in history: %v\n", err)
			}
		}

		nextVersion++
	}

	// Versioned migrations are all up-to-date.  Now run repeatable migrations.
	if r.repeatableDir != "" && r.repeatableStore != nil {
		if err := r.runRepeatableMigrations(); err != nil {
			return err
		}
	}

	return nil
}

// runRepeatableMigrations ensures the tracking table exists, loads all repeatable migration files
// from r.repeatableDir, and executes any whose content has changed since the last successful run.
func (r *Runner) runRepeatableMigrations() error {
	if err := r.repeatableStore.EnsureRepeatableHistoryTable(); err != nil {
		return fmt.Errorf("failed to ensure repeatable history table: %w", err)
	}

	migrations, err := loadRepeatableMigrations(r.migrations, r.repeatableDir)
	if err != nil {
		return fmt.Errorf("failed to load repeatable migrations: %w", err)
	}

	return runRepeatableMigrationsWithStore(r.db, r.repeatableStore, migrations)
}

func (r *Runner) createMigrator() (*migrate.Migrate, error) {
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
