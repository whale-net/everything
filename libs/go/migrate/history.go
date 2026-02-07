package migrate

import (
	"database/sql"
	"fmt"
	"time"
)

// HistoryTracker tracks migration attempts in the migration_history table
type HistoryTracker struct {
	db *sql.DB
}

// HistoryEntry represents a single migration history record
type HistoryEntry struct {
	HistoryID    int64
	Version      int64
	Direction    string
	Status       string
	StartedAt    time.Time
	CompletedAt  *time.Time
	DurationMs   *int
	ErrorMessage *string
	AppliedBy    string
	CreatedAt    time.Time
}

// NewHistoryTracker creates a new history tracker
func NewHistoryTracker(db *sql.DB) *HistoryTracker {
	return &HistoryTracker{db: db}
}

// EnsureHistoryTable creates the migration_history table if it doesn't exist
func (h *HistoryTracker) EnsureHistoryTable() error {
	query := `
	CREATE TABLE IF NOT EXISTS migration_history (
		history_id BIGSERIAL PRIMARY KEY,
		version BIGINT NOT NULL,
		direction VARCHAR(4) NOT NULL CHECK (direction IN ('up', 'down')),
		status VARCHAR(20) NOT NULL CHECK (status IN ('started', 'success', 'failed')),
		started_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		completed_at TIMESTAMP,
		duration_ms INTEGER,
		error_message TEXT,
		applied_by VARCHAR(255) DEFAULT 'migration-binary',
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_migration_history_version ON migration_history(version);
	CREATE INDEX IF NOT EXISTS idx_migration_history_status ON migration_history(status);
	CREATE INDEX IF NOT EXISTS idx_migration_history_started_at ON migration_history(started_at);
	CREATE INDEX IF NOT EXISTS idx_migration_history_version_direction ON migration_history(version, direction);
	`

	_, err := h.db.Exec(query)
	if err != nil {
		return fmt.Errorf("failed to create migration_history table: %w", err)
	}

	return nil
}

// RecordStart logs the start of a migration attempt
func (h *HistoryTracker) RecordStart(version int64, direction string) (int64, error) {
	query := `
		INSERT INTO migration_history (version, direction, status, started_at, applied_by)
		VALUES ($1, $2, 'started', $3, 'migration-binary')
		RETURNING history_id
	`

	var historyID int64
	err := h.db.QueryRow(query, version, direction, time.Now()).Scan(&historyID)
	if err != nil {
		return 0, fmt.Errorf("failed to record migration start: %w", err)
	}

	return historyID, nil
}

// RecordSuccess marks a migration as successful
func (h *HistoryTracker) RecordSuccess(historyID int64, startTime time.Time) error {
	duration := int(time.Since(startTime).Milliseconds())
	query := `
		UPDATE migration_history
		SET status = 'success', completed_at = $1, duration_ms = $2
		WHERE history_id = $3
	`

	_, err := h.db.Exec(query, time.Now(), duration, historyID)
	if err != nil {
		return fmt.Errorf("failed to record migration success: %w", err)
	}

	return nil
}

// RecordFailure marks a migration as failed
func (h *HistoryTracker) RecordFailure(historyID int64, startTime time.Time, migrationError error) error {
	duration := int(time.Since(startTime).Milliseconds())
	errorMsg := migrationError.Error()
	query := `
		UPDATE migration_history
		SET status = 'failed', completed_at = $1, duration_ms = $2, error_message = $3
		WHERE history_id = $4
	`

	_, err := h.db.Exec(query, time.Now(), duration, errorMsg, historyID)
	if err != nil {
		return fmt.Errorf("failed to record migration failure: %w", err)
	}

	return nil
}

// GetSuccessfulMigrations returns all successfully completed migrations
func (h *HistoryTracker) GetSuccessfulMigrations() ([]int64, error) {
	query := `
		SELECT DISTINCT version
		FROM migration_history
		WHERE status = 'success'
		ORDER BY version
	`

	rows, err := h.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to get successful migrations: %w", err)
	}
	defer rows.Close()

	var versions []int64
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("failed to scan version: %w", err)
		}
		versions = append(versions, version)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating versions: %w", err)
	}

	return versions, nil
}

// GetLastAttempt returns the most recent attempt for a specific version
func (h *HistoryTracker) GetLastAttempt(version int64) (*HistoryEntry, error) {
	query := `
		SELECT history_id, version, direction, status, started_at, completed_at,
		       duration_ms, error_message, applied_by, created_at
		FROM migration_history
		WHERE version = $1
		ORDER BY started_at DESC
		LIMIT 1
	`

	entry := &HistoryEntry{}
	err := h.db.QueryRow(query, version).Scan(
		&entry.HistoryID,
		&entry.Version,
		&entry.Direction,
		&entry.Status,
		&entry.StartedAt,
		&entry.CompletedAt,
		&entry.DurationMs,
		&entry.ErrorMessage,
		&entry.AppliedBy,
		&entry.CreatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get last attempt: %w", err)
	}

	return entry, nil
}

// ValidateRecovery checks if forcing to a version is safe based on history
func (h *HistoryTracker) ValidateRecovery(version int64) error {
	lastAttempt, err := h.GetLastAttempt(version)
	if err != nil {
		return fmt.Errorf("failed to validate recovery: %w", err)
	}

	if lastAttempt == nil {
		// No history for this version - allow force
		return nil
	}

	switch lastAttempt.Status {
	case "failed":
		return fmt.Errorf("version %d last attempt FAILED with error: %s\nUse --force-dangerous to override (not recommended)",
			version, stringOrEmpty(lastAttempt.ErrorMessage))
	case "started":
		return fmt.Errorf("version %d migration was INTERRUPTED (started but never completed)\nUse --force-dangerous to override", version)
	case "success":
		// Success is fine - allow force
		return nil
	default:
		return fmt.Errorf("version %d has unknown status: %s", version, lastAttempt.Status)
	}
}

// GetHistory retrieves migration history with optional limit
func (h *HistoryTracker) GetHistory(limit int) ([]HistoryEntry, error) {
	query := `
		SELECT history_id, version, direction, status, started_at, completed_at,
		       duration_ms, error_message, applied_by, created_at
		FROM migration_history
		ORDER BY started_at DESC
		LIMIT $1
	`

	if limit <= 0 {
		limit = 20
	}

	rows, err := h.db.Query(query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get history: %w", err)
	}
	defer rows.Close()

	var entries []HistoryEntry
	for rows.Next() {
		var entry HistoryEntry
		if err := rows.Scan(
			&entry.HistoryID,
			&entry.Version,
			&entry.Direction,
			&entry.Status,
			&entry.StartedAt,
			&entry.CompletedAt,
			&entry.DurationMs,
			&entry.ErrorMessage,
			&entry.AppliedBy,
			&entry.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan history entry: %w", err)
		}
		entries = append(entries, entry)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating history: %w", err)
	}

	return entries, nil
}

func stringOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
