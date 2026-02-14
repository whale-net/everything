package migrate

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestHistoryRepo(t *testing.T) (*HistoryRepo, *sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	tracker := NewHistoryTracker(db)
	repo := NewHistoryRepo(tracker)
	return repo, db, mock
}

func TestNewHistoryRepo(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	tracker := NewHistoryTracker(db)
	repo := NewHistoryRepo(tracker)
	assert.NotNil(t, repo)
	assert.Equal(t, tracker, repo.tracker)
}

func TestGetStatus(t *testing.T) {
	t.Run("never attempted version is safe", func(t *testing.T) {
		repo, db, mock := newTestHistoryRepo(t)
		defer db.Close()

		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(int64(10)).
			WillReturnError(sql.ErrNoRows)

		status, err := repo.GetStatus(10)
		assert.NoError(t, err)
		require.NotNil(t, status)
		assert.Equal(t, int64(10), status.Version)
		assert.False(t, status.HasBeenAttempted)
		assert.True(t, status.IsSafe)
		assert.Equal(t, "", status.LastStatus)
		assert.Equal(t, "", status.LastError)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("successful version is safe", func(t *testing.T) {
		repo, db, mock := newTestHistoryRepo(t)
		defer db.Close()

		now := time.Now()
		rows := sqlmock.NewRows([]string{
			"history_id", "version", "direction", "status",
			"started_at", "completed_at", "duration_ms",
			"error_message", "applied_by", "created_at",
		}).AddRow(
			int64(5), int64(3), "up", "success",
			now, now, 100, nil, "migration-binary", now,
		)

		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(int64(3)).
			WillReturnRows(rows)

		status, err := repo.GetStatus(3)
		assert.NoError(t, err)
		require.NotNil(t, status)
		assert.Equal(t, int64(3), status.Version)
		assert.True(t, status.HasBeenAttempted)
		assert.True(t, status.IsSafe)
		assert.Equal(t, "success", status.LastStatus)
		assert.Equal(t, "", status.LastError)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("failed version is not safe", func(t *testing.T) {
		repo, db, mock := newTestHistoryRepo(t)
		defer db.Close()

		now := time.Now()
		errMsg := "column already exists"
		rows := sqlmock.NewRows([]string{
			"history_id", "version", "direction", "status",
			"started_at", "completed_at", "duration_ms",
			"error_message", "applied_by", "created_at",
		}).AddRow(
			int64(5), int64(3), "up", "failed",
			now, now, 50, errMsg, "migration-binary", now,
		)

		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(int64(3)).
			WillReturnRows(rows)

		status, err := repo.GetStatus(3)
		assert.NoError(t, err)
		require.NotNil(t, status)
		assert.True(t, status.HasBeenAttempted)
		assert.False(t, status.IsSafe)
		assert.Equal(t, "failed", status.LastStatus)
		assert.Equal(t, "column already exists", status.LastError)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("started (interrupted) version is not safe", func(t *testing.T) {
		repo, db, mock := newTestHistoryRepo(t)
		defer db.Close()

		now := time.Now()
		rows := sqlmock.NewRows([]string{
			"history_id", "version", "direction", "status",
			"started_at", "completed_at", "duration_ms",
			"error_message", "applied_by", "created_at",
		}).AddRow(
			int64(5), int64(3), "up", "started",
			now, nil, nil, nil, "migration-binary", now,
		)

		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(int64(3)).
			WillReturnRows(rows)

		status, err := repo.GetStatus(3)
		assert.NoError(t, err)
		require.NotNil(t, status)
		assert.True(t, status.HasBeenAttempted)
		assert.False(t, status.IsSafe)
		assert.Equal(t, "started", status.LastStatus)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error on DB failure", func(t *testing.T) {
		repo, db, mock := newTestHistoryRepo(t)
		defer db.Close()

		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(int64(1)).
			WillReturnError(fmt.Errorf("connection refused"))

		status, err := repo.GetStatus(1)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get migration status")
		assert.Nil(t, status)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestIsVersionSafe(t *testing.T) {
	t.Run("safe when never attempted", func(t *testing.T) {
		repo, db, mock := newTestHistoryRepo(t)
		defer db.Close()

		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(int64(5)).
			WillReturnError(sql.ErrNoRows)

		safe, reason, err := repo.IsVersionSafe(5)
		assert.NoError(t, err)
		assert.True(t, safe)
		assert.Equal(t, "", reason)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("safe when last attempt succeeded", func(t *testing.T) {
		repo, db, mock := newTestHistoryRepo(t)
		defer db.Close()

		now := time.Now()
		rows := sqlmock.NewRows([]string{
			"history_id", "version", "direction", "status",
			"started_at", "completed_at", "duration_ms",
			"error_message", "applied_by", "created_at",
		}).AddRow(
			int64(1), int64(5), "up", "success",
			now, now, 100, nil, "migration-binary", now,
		)

		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(int64(5)).
			WillReturnRows(rows)

		safe, reason, err := repo.IsVersionSafe(5)
		assert.NoError(t, err)
		assert.True(t, safe)
		assert.Equal(t, "", reason)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("not safe when last attempt failed", func(t *testing.T) {
		repo, db, mock := newTestHistoryRepo(t)
		defer db.Close()

		now := time.Now()
		errMsg := "table not found"
		rows := sqlmock.NewRows([]string{
			"history_id", "version", "direction", "status",
			"started_at", "completed_at", "duration_ms",
			"error_message", "applied_by", "created_at",
		}).AddRow(
			int64(1), int64(5), "up", "failed",
			now, now, 50, errMsg, "migration-binary", now,
		)

		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(int64(5)).
			WillReturnRows(rows)

		safe, reason, err := repo.IsVersionSafe(5)
		assert.NoError(t, err)
		assert.False(t, safe)
		assert.Contains(t, reason, "Last attempt failed")
		assert.Contains(t, reason, "table not found")
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("not safe when migration was interrupted", func(t *testing.T) {
		repo, db, mock := newTestHistoryRepo(t)
		defer db.Close()

		now := time.Now()
		rows := sqlmock.NewRows([]string{
			"history_id", "version", "direction", "status",
			"started_at", "completed_at", "duration_ms",
			"error_message", "applied_by", "created_at",
		}).AddRow(
			int64(1), int64(5), "up", "started",
			now, nil, nil, nil, "migration-binary", now,
		)

		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(int64(5)).
			WillReturnRows(rows)

		safe, reason, err := repo.IsVersionSafe(5)
		assert.NoError(t, err)
		assert.False(t, safe)
		assert.Contains(t, reason, "interrupted")
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("not safe on unknown status", func(t *testing.T) {
		repo, db, mock := newTestHistoryRepo(t)
		defer db.Close()

		now := time.Now()
		rows := sqlmock.NewRows([]string{
			"history_id", "version", "direction", "status",
			"started_at", "completed_at", "duration_ms",
			"error_message", "applied_by", "created_at",
		}).AddRow(
			int64(1), int64(5), "up", "weird_status",
			now, nil, nil, nil, "migration-binary", now,
		)

		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(int64(5)).
			WillReturnRows(rows)

		safe, reason, err := repo.IsVersionSafe(5)
		assert.NoError(t, err)
		assert.False(t, safe)
		assert.Contains(t, reason, "Unknown status")
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error on DB failure", func(t *testing.T) {
		repo, db, mock := newTestHistoryRepo(t)
		defer db.Close()

		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(int64(5)).
			WillReturnError(fmt.Errorf("connection lost"))

		safe, reason, err := repo.IsVersionSafe(5)
		assert.Error(t, err)
		assert.False(t, safe)
		assert.Equal(t, "", reason)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestGetRecentHistory(t *testing.T) {
	t.Run("delegates to tracker GetHistory", func(t *testing.T) {
		repo, db, mock := newTestHistoryRepo(t)
		defer db.Close()

		now := time.Now()
		rows := sqlmock.NewRows([]string{
			"history_id", "version", "direction", "status",
			"started_at", "completed_at", "duration_ms",
			"error_message", "applied_by", "created_at",
		}).AddRow(
			int64(1), int64(1), "up", "success",
			now, now, 100, nil, "migration-binary", now,
		)

		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(5).
			WillReturnRows(rows)

		entries, err := repo.GetRecentHistory(5)
		assert.NoError(t, err)
		assert.Len(t, entries, 1)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestGetSuccessfulVersions(t *testing.T) {
	t.Run("delegates to tracker GetSuccessfulMigrations", func(t *testing.T) {
		repo, db, mock := newTestHistoryRepo(t)
		defer db.Close()

		rows := sqlmock.NewRows([]string{"version"}).
			AddRow(int64(1)).
			AddRow(int64(2))
		mock.ExpectQuery("SELECT DISTINCT version FROM migration_history").
			WillReturnRows(rows)

		versions, err := repo.GetSuccessfulVersions()
		assert.NoError(t, err)
		assert.Equal(t, []int64{1, 2}, versions)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestHasAnyHistory(t *testing.T) {
	t.Run("returns true when history exists", func(t *testing.T) {
		repo, db, mock := newTestHistoryRepo(t)
		defer db.Close()

		now := time.Now()
		rows := sqlmock.NewRows([]string{
			"history_id", "version", "direction", "status",
			"started_at", "completed_at", "duration_ms",
			"error_message", "applied_by", "created_at",
		}).AddRow(
			int64(1), int64(1), "up", "success",
			now, now, 100, nil, "migration-binary", now,
		)

		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(1).
			WillReturnRows(rows)

		hasHistory, err := repo.HasAnyHistory()
		assert.NoError(t, err)
		assert.True(t, hasHistory)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns false when no history", func(t *testing.T) {
		repo, db, mock := newTestHistoryRepo(t)
		defer db.Close()

		rows := sqlmock.NewRows([]string{
			"history_id", "version", "direction", "status",
			"started_at", "completed_at", "duration_ms",
			"error_message", "applied_by", "created_at",
		})

		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(1).
			WillReturnRows(rows)

		hasHistory, err := repo.HasAnyHistory()
		assert.NoError(t, err)
		assert.False(t, hasHistory)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error on DB failure", func(t *testing.T) {
		repo, db, mock := newTestHistoryRepo(t)
		defer db.Close()

		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(1).
			WillReturnError(fmt.Errorf("table not found"))

		hasHistory, err := repo.HasAnyHistory()
		assert.Error(t, err)
		assert.False(t, hasHistory)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}
