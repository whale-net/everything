package migrate

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"
)

// RepeatableMigration represents a single repeatable migration file loaded from the filesystem.
// Files must be named with the "R__" prefix and end in ".sql" (e.g. "R__seed_views.sql").
type RepeatableMigration struct {
	Name     string
	Checksum string
	Content  []byte
}

// RepeatableHistoryEntry represents a single row in the repeatable_migration_history table.
type RepeatableHistoryEntry struct {
	HistoryID    int64
	Name         string
	Checksum     string
	Status       string
	StartedAt    time.Time
	CompletedAt  *time.Time
	DurationMs   *int
	ErrorMessage *string
	AppliedBy    string
	CreatedAt    time.Time
}

// RepeatableStore is the interface for repeatable migration tracking operations.
// Implementations must be safe to use from a single goroutine at a time.
type RepeatableStore interface {
	// EnsureRepeatableHistoryTable creates the repeatable_migration_history table if it does not exist.
	EnsureRepeatableHistoryTable() error
	// GetLastSuccessfulChecksum returns the checksum of the most recent successful run for the
	// given migration name. Returns "" when no successful run has been recorded.
	GetLastSuccessfulChecksum(name string) (string, error)
	// RecordStart inserts a new "started" row and returns its history_id.
	RecordStart(name, checksum string) (int64, error)
	// RecordSuccess updates the row identified by historyID to status "success".
	RecordSuccess(historyID int64, startTime time.Time) error
	// RecordFailure updates the row identified by historyID to status "failed".
	RecordFailure(historyID int64, startTime time.Time, migrationError error) error
}

// RepeatableTracker is the production implementation of RepeatableStore backed by a *sql.DB.
type RepeatableTracker struct {
	db *sql.DB
}

// NewRepeatableTracker creates a new RepeatableTracker.
func NewRepeatableTracker(db *sql.DB) *RepeatableTracker {
	return &RepeatableTracker{db: db}
}

// EnsureRepeatableHistoryTable creates the repeatable_migration_history table if it does not exist.
// This is safe to call multiple times (idempotent).
func (r *RepeatableTracker) EnsureRepeatableHistoryTable() error {
	query := `
	CREATE TABLE IF NOT EXISTS repeatable_migration_history (
		history_id   BIGSERIAL PRIMARY KEY,
		name         VARCHAR(255) NOT NULL,
		checksum     VARCHAR(64)  NOT NULL,
		status       VARCHAR(20)  NOT NULL CHECK (status IN ('started', 'success', 'failed')),
		started_at   TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
		completed_at TIMESTAMP,
		duration_ms  INTEGER,
		error_message TEXT,
		applied_by   VARCHAR(255) DEFAULT 'migration-binary',
		created_at   TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_repeatable_migration_history_name
		ON repeatable_migration_history(name);
	CREATE INDEX IF NOT EXISTS idx_repeatable_migration_history_status
		ON repeatable_migration_history(status);
	CREATE INDEX IF NOT EXISTS idx_repeatable_migration_history_name_checksum
		ON repeatable_migration_history(name, checksum);
	`

	_, err := r.db.Exec(query)
	if err != nil {
		return fmt.Errorf("failed to create repeatable_migration_history table: %w", err)
	}

	return nil
}

// GetLastSuccessfulChecksum returns the checksum of the most recent successful run for the given name.
func (r *RepeatableTracker) GetLastSuccessfulChecksum(name string) (string, error) {
	query := `
		SELECT checksum
		FROM repeatable_migration_history
		WHERE name = $1 AND status = 'success'
		ORDER BY completed_at DESC
		LIMIT 1
	`

	var checksum string
	err := r.db.QueryRow(query, name).Scan(&checksum)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to get last successful checksum for %q: %w", name, err)
	}

	return checksum, nil
}

// RecordStart inserts a new "started" row and returns its history_id.
func (r *RepeatableTracker) RecordStart(name, checksum string) (int64, error) {
	query := `
		INSERT INTO repeatable_migration_history (name, checksum, status, started_at, applied_by)
		VALUES ($1, $2, 'started', $3, 'migration-binary')
		RETURNING history_id
	`

	var historyID int64
	err := r.db.QueryRow(query, name, checksum, time.Now()).Scan(&historyID)
	if err != nil {
		return 0, fmt.Errorf("failed to record repeatable migration start: %w", err)
	}

	return historyID, nil
}

// RecordSuccess marks the row identified by historyID as successful.
func (r *RepeatableTracker) RecordSuccess(historyID int64, startTime time.Time) error {
	duration := int(time.Since(startTime).Milliseconds())
	query := `
		UPDATE repeatable_migration_history
		SET status = 'success', completed_at = $1, duration_ms = $2
		WHERE history_id = $3
	`

	_, err := r.db.Exec(query, time.Now(), duration, historyID)
	if err != nil {
		return fmt.Errorf("failed to record repeatable migration success: %w", err)
	}

	return nil
}

// RecordFailure marks the row identified by historyID as failed.
func (r *RepeatableTracker) RecordFailure(historyID int64, startTime time.Time, migrationError error) error {
	duration := int(time.Since(startTime).Milliseconds())
	errorMsg := migrationError.Error()
	query := `
		UPDATE repeatable_migration_history
		SET status = 'failed', completed_at = $1, duration_ms = $2, error_message = $3
		WHERE history_id = $4
	`

	_, err := r.db.Exec(query, time.Now(), duration, errorMsg, historyID)
	if err != nil {
		return fmt.Errorf("failed to record repeatable migration failure: %w", err)
	}

	return nil
}

// computeChecksum returns the hex-encoded SHA-256 digest of content.
func computeChecksum(content []byte) string {
	sum := sha256.Sum256(content)
	return fmt.Sprintf("%x", sum)
}

// loadRepeatableMigrations reads all repeatable migration files from dir inside migrations.
// Only files whose name starts with "R__" and ends with ".sql" are loaded.
// The returned slice is sorted alphabetically by file name so execution order is deterministic.
// The function accepts fs.FS so it is testable with testing/fstest.MapFS.
func loadRepeatableMigrations(migrations fs.FS, dir string) ([]RepeatableMigration, error) {
	entries, err := fs.ReadDir(migrations, dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read repeatable migrations directory %q: %w", dir, err)
	}

	var result []RepeatableMigration
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasPrefix(name, "R__") || !strings.HasSuffix(name, ".sql") {
			continue
		}

		content, err := fs.ReadFile(migrations, dir+"/"+name)
		if err != nil {
			return nil, fmt.Errorf("failed to read repeatable migration %q: %w", name, err)
		}

		result = append(result, RepeatableMigration{
			Name:     name,
			Checksum: computeChecksum(content),
			Content:  content,
		})
	}

	// Stable, deterministic ordering.
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})

	return result, nil
}

// sqlExecutor is the minimal interface needed to execute SQL statements.
// *sql.DB satisfies this interface.
type sqlExecutor interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
}

// runRepeatableMigrationsWithStore executes each migration in migrations using exec for the SQL
// and store for history tracking.  A migration is skipped when its current checksum matches the
// checksum of the most recent successful run recorded in the store.
func runRepeatableMigrationsWithStore(exec sqlExecutor, store RepeatableStore, migrations []RepeatableMigration) error {
	for _, m := range migrations {
		lastChecksum, err := store.GetLastSuccessfulChecksum(m.Name)
		if err != nil {
			return fmt.Errorf("failed to check last checksum for %q: %w", m.Name, err)
		}

		if lastChecksum == m.Checksum {
			fmt.Printf("~ Repeatable migration %q unchanged (checksum %.8s…), skipping\n", m.Name, m.Checksum)
			continue
		}

		startTime := time.Now()

		historyID, recErr := store.RecordStart(m.Name, m.Checksum)
		if recErr != nil {
			fmt.Printf("Warning: failed to record repeatable migration start in history: %v\n", recErr)
		}

		_, execErr := exec.Exec(string(m.Content))
		if execErr != nil {
			fmt.Printf("✗ Repeatable migration %q failed: %v\n", m.Name, execErr)
			if historyID > 0 {
				if recErr := store.RecordFailure(historyID, startTime, execErr); recErr != nil {
					fmt.Printf("Warning: failed to record repeatable migration failure in history: %v\n", recErr)
				}
			}
			return fmt.Errorf("repeatable migration %q failed: %w", m.Name, execErr)
		}

		fmt.Printf("✓ Repeatable migration %q completed (%dms)\n", m.Name, time.Since(startTime).Milliseconds())
		if historyID > 0 {
			if recErr := store.RecordSuccess(historyID, startTime); recErr != nil {
				fmt.Printf("Warning: failed to record repeatable migration success in history: %v\n", recErr)
			}
		}
	}

	return nil
}
