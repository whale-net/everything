package migrate

import (
	"fmt"
)

// HistoryRepo provides a simplified interface for accessing migration history
type HistoryRepo struct {
	tracker *HistoryTracker
}

// NewHistoryRepo creates a new history repository
func NewHistoryRepo(tracker *HistoryTracker) *HistoryRepo {
	return &HistoryRepo{tracker: tracker}
}

// MigrationStatus represents the current status of a specific migration version
type MigrationStatus struct {
	Version         int64
	HasBeenAttempted bool
	LastStatus      string // "success", "failed", "started", or "" if never attempted
	LastError       string
	IsSafe          bool   // true if last attempt was successful or never attempted
}

// GetStatus returns the status of a specific migration version
func (r *HistoryRepo) GetStatus(version int64) (*MigrationStatus, error) {
	lastAttempt, err := r.tracker.GetLastAttempt(version)
	if err != nil {
		return nil, fmt.Errorf("failed to get migration status: %w", err)
	}

	status := &MigrationStatus{
		Version:         version,
		HasBeenAttempted: lastAttempt != nil,
	}

	if lastAttempt == nil {
		status.IsSafe = true
		return status, nil
	}

	status.LastStatus = lastAttempt.Status
	if lastAttempt.ErrorMessage != nil {
		status.LastError = *lastAttempt.ErrorMessage
	}

	// Safe if last attempt was successful
	status.IsSafe = lastAttempt.Status == "success"

	return status, nil
}

// IsVersionSafe checks if a version is safe to force to
func (r *HistoryRepo) IsVersionSafe(version int64) (bool, string, error) {
	status, err := r.GetStatus(version)
	if err != nil {
		return false, "", err
	}

	if status.IsSafe {
		return true, "", nil
	}

	switch status.LastStatus {
	case "failed":
		return false, fmt.Sprintf("Last attempt failed: %s", status.LastError), nil
	case "started":
		return false, "Migration was interrupted (never completed)", nil
	default:
		return false, fmt.Sprintf("Unknown status: %s", status.LastStatus), nil
	}
}

// GetRecentHistory returns the N most recent migration attempts
func (r *HistoryRepo) GetRecentHistory(limit int) ([]HistoryEntry, error) {
	return r.tracker.GetHistory(limit)
}

// GetSuccessfulVersions returns all versions that have completed successfully
func (r *HistoryRepo) GetSuccessfulVersions() ([]int64, error) {
	return r.tracker.GetSuccessfulMigrations()
}

// HasAnyHistory checks if there is any migration history recorded
func (r *HistoryRepo) HasAnyHistory() (bool, error) {
	entries, err := r.tracker.GetHistory(1)
	if err != nil {
		return false, err
	}
	return len(entries) > 0, nil
}
