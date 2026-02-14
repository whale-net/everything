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

func TestNewHistoryTracker(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	tracker := NewHistoryTracker(db)
	assert.NotNil(t, tracker)
	assert.Equal(t, db, tracker.db)
}

func TestEnsureHistoryTable(t *testing.T) {
	t.Run("creates table successfully", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		mock.ExpectExec("CREATE TABLE IF NOT EXISTS migration_history").
			WillReturnResult(sqlmock.NewResult(0, 0))

		tracker := NewHistoryTracker(db)
		err = tracker.EnsureHistoryTable()
		assert.NoError(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error on failure", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		mock.ExpectExec("CREATE TABLE IF NOT EXISTS migration_history").
			WillReturnError(fmt.Errorf("permission denied"))

		tracker := NewHistoryTracker(db)
		err = tracker.EnsureHistoryTable()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to create migration_history table")
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestRecordStart(t *testing.T) {
	t.Run("records start and returns history ID", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		mock.ExpectQuery("INSERT INTO migration_history").
			WithArgs(int64(1), "up", sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows([]string{"history_id"}).AddRow(int64(42)))

		tracker := NewHistoryTracker(db)
		historyID, err := tracker.RecordStart(1, "up")
		assert.NoError(t, err)
		assert.Equal(t, int64(42), historyID)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("records down direction", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		mock.ExpectQuery("INSERT INTO migration_history").
			WithArgs(int64(5), "down", sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows([]string{"history_id"}).AddRow(int64(99)))

		tracker := NewHistoryTracker(db)
		historyID, err := tracker.RecordStart(5, "down")
		assert.NoError(t, err)
		assert.Equal(t, int64(99), historyID)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error on DB failure", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		mock.ExpectQuery("INSERT INTO migration_history").
			WithArgs(int64(1), "up", sqlmock.AnyArg()).
			WillReturnError(fmt.Errorf("connection lost"))

		tracker := NewHistoryTracker(db)
		historyID, err := tracker.RecordStart(1, "up")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to record migration start")
		assert.Equal(t, int64(0), historyID)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestRecordSuccess(t *testing.T) {
	t.Run("records success with duration", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		mock.ExpectExec("UPDATE migration_history").
			WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), int64(42)).
			WillReturnResult(sqlmock.NewResult(0, 1))

		tracker := NewHistoryTracker(db)
		startTime := time.Now().Add(-100 * time.Millisecond)
		err = tracker.RecordSuccess(42, startTime)
		assert.NoError(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error on DB failure", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		mock.ExpectExec("UPDATE migration_history").
			WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), int64(42)).
			WillReturnError(fmt.Errorf("connection lost"))

		tracker := NewHistoryTracker(db)
		err = tracker.RecordSuccess(42, time.Now())
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to record migration success")
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestRecordFailure(t *testing.T) {
	t.Run("records failure with error message", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		migrationErr := fmt.Errorf("syntax error in migration")
		mock.ExpectExec("UPDATE migration_history").
			WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), migrationErr.Error(), int64(7)).
			WillReturnResult(sqlmock.NewResult(0, 1))

		tracker := NewHistoryTracker(db)
		startTime := time.Now().Add(-50 * time.Millisecond)
		err = tracker.RecordFailure(7, startTime, migrationErr)
		assert.NoError(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error on DB failure", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		mock.ExpectExec("UPDATE migration_history").
			WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), int64(7)).
			WillReturnError(fmt.Errorf("disk full"))

		tracker := NewHistoryTracker(db)
		err = tracker.RecordFailure(7, time.Now(), fmt.Errorf("original error"))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to record migration failure")
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestGetSuccessfulMigrations(t *testing.T) {
	t.Run("returns successful versions", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		rows := sqlmock.NewRows([]string{"version"}).
			AddRow(int64(1)).
			AddRow(int64(2)).
			AddRow(int64(3))
		mock.ExpectQuery("SELECT DISTINCT version FROM migration_history").
			WillReturnRows(rows)

		tracker := NewHistoryTracker(db)
		versions, err := tracker.GetSuccessfulMigrations()
		assert.NoError(t, err)
		assert.Equal(t, []int64{1, 2, 3}, versions)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns empty slice when no successful migrations", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		rows := sqlmock.NewRows([]string{"version"})
		mock.ExpectQuery("SELECT DISTINCT version FROM migration_history").
			WillReturnRows(rows)

		tracker := NewHistoryTracker(db)
		versions, err := tracker.GetSuccessfulMigrations()
		assert.NoError(t, err)
		assert.Nil(t, versions)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error on query failure", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		mock.ExpectQuery("SELECT DISTINCT version FROM migration_history").
			WillReturnError(fmt.Errorf("table does not exist"))

		tracker := NewHistoryTracker(db)
		versions, err := tracker.GetSuccessfulMigrations()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get successful migrations")
		assert.Nil(t, versions)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestGetLastAttempt(t *testing.T) {
	t.Run("returns last attempt for version", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		now := time.Now()
		completedAt := now.Add(1 * time.Second)
		durationMs := 1000
		rows := sqlmock.NewRows([]string{
			"history_id", "version", "direction", "status",
			"started_at", "completed_at", "duration_ms",
			"error_message", "applied_by", "created_at",
		}).AddRow(
			int64(10), int64(3), "up", "success",
			now, completedAt, durationMs,
			nil, "migration-binary", now,
		)

		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(int64(3)).
			WillReturnRows(rows)

		tracker := NewHistoryTracker(db)
		entry, err := tracker.GetLastAttempt(3)
		assert.NoError(t, err)
		require.NotNil(t, entry)
		assert.Equal(t, int64(10), entry.HistoryID)
		assert.Equal(t, int64(3), entry.Version)
		assert.Equal(t, "up", entry.Direction)
		assert.Equal(t, "success", entry.Status)
		assert.NotNil(t, entry.CompletedAt)
		assert.NotNil(t, entry.DurationMs)
		assert.Equal(t, 1000, *entry.DurationMs)
		assert.Nil(t, entry.ErrorMessage)
		assert.Equal(t, "migration-binary", entry.AppliedBy)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns nil when no history for version", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(int64(99)).
			WillReturnError(sql.ErrNoRows)

		tracker := NewHistoryTracker(db)
		entry, err := tracker.GetLastAttempt(99)
		assert.NoError(t, err)
		assert.Nil(t, entry)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns failed attempt with error message", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		now := time.Now()
		errMsg := "syntax error at line 5"
		durationMs := 50
		rows := sqlmock.NewRows([]string{
			"history_id", "version", "direction", "status",
			"started_at", "completed_at", "duration_ms",
			"error_message", "applied_by", "created_at",
		}).AddRow(
			int64(15), int64(4), "up", "failed",
			now, now, durationMs,
			errMsg, "migration-binary", now,
		)

		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(int64(4)).
			WillReturnRows(rows)

		tracker := NewHistoryTracker(db)
		entry, err := tracker.GetLastAttempt(4)
		assert.NoError(t, err)
		require.NotNil(t, entry)
		assert.Equal(t, "failed", entry.Status)
		require.NotNil(t, entry.ErrorMessage)
		assert.Equal(t, "syntax error at line 5", *entry.ErrorMessage)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error on query failure", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(int64(1)).
			WillReturnError(fmt.Errorf("connection refused"))

		tracker := NewHistoryTracker(db)
		entry, err := tracker.GetLastAttempt(1)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get last attempt")
		assert.Nil(t, entry)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestValidateRecovery(t *testing.T) {
	t.Run("allows force when no history exists", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(int64(5)).
			WillReturnError(sql.ErrNoRows)

		tracker := NewHistoryTracker(db)
		err = tracker.ValidateRecovery(5)
		assert.NoError(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("allows force when last attempt was successful", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		now := time.Now()
		rows := sqlmock.NewRows([]string{
			"history_id", "version", "direction", "status",
			"started_at", "completed_at", "duration_ms",
			"error_message", "applied_by", "created_at",
		}).AddRow(
			int64(10), int64(3), "up", "success",
			now, now, 100, nil, "migration-binary", now,
		)

		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(int64(3)).
			WillReturnRows(rows)

		tracker := NewHistoryTracker(db)
		err = tracker.ValidateRecovery(3)
		assert.NoError(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("rejects force when last attempt failed", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		now := time.Now()
		errMsg := "column does not exist"
		rows := sqlmock.NewRows([]string{
			"history_id", "version", "direction", "status",
			"started_at", "completed_at", "duration_ms",
			"error_message", "applied_by", "created_at",
		}).AddRow(
			int64(10), int64(3), "up", "failed",
			now, now, 100, errMsg, "migration-binary", now,
		)

		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(int64(3)).
			WillReturnRows(rows)

		tracker := NewHistoryTracker(db)
		err = tracker.ValidateRecovery(3)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "FAILED")
		assert.Contains(t, err.Error(), "column does not exist")
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("rejects force when last attempt was interrupted", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		now := time.Now()
		rows := sqlmock.NewRows([]string{
			"history_id", "version", "direction", "status",
			"started_at", "completed_at", "duration_ms",
			"error_message", "applied_by", "created_at",
		}).AddRow(
			int64(10), int64(3), "up", "started",
			now, nil, nil, nil, "migration-binary", now,
		)

		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(int64(3)).
			WillReturnRows(rows)

		tracker := NewHistoryTracker(db)
		err = tracker.ValidateRecovery(3)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "INTERRUPTED")
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("rejects force on unknown status", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		now := time.Now()
		rows := sqlmock.NewRows([]string{
			"history_id", "version", "direction", "status",
			"started_at", "completed_at", "duration_ms",
			"error_message", "applied_by", "created_at",
		}).AddRow(
			int64(10), int64(3), "up", "unknown_status",
			now, nil, nil, nil, "migration-binary", now,
		)

		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(int64(3)).
			WillReturnRows(rows)

		tracker := NewHistoryTracker(db)
		err = tracker.ValidateRecovery(3)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unknown status")
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestGetHistory(t *testing.T) {
	t.Run("returns history entries", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		now := time.Now()
		rows := sqlmock.NewRows([]string{
			"history_id", "version", "direction", "status",
			"started_at", "completed_at", "duration_ms",
			"error_message", "applied_by", "created_at",
		}).
			AddRow(int64(2), int64(2), "up", "success", now, now, 200, nil, "migration-binary", now).
			AddRow(int64(1), int64(1), "up", "success", now, now, 100, nil, "migration-binary", now)

		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(10).
			WillReturnRows(rows)

		tracker := NewHistoryTracker(db)
		entries, err := tracker.GetHistory(10)
		assert.NoError(t, err)
		assert.Len(t, entries, 2)
		assert.Equal(t, int64(2), entries[0].HistoryID)
		assert.Equal(t, int64(1), entries[1].HistoryID)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("defaults to 20 when limit is zero", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		rows := sqlmock.NewRows([]string{
			"history_id", "version", "direction", "status",
			"started_at", "completed_at", "duration_ms",
			"error_message", "applied_by", "created_at",
		})
		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(20).
			WillReturnRows(rows)

		tracker := NewHistoryTracker(db)
		entries, err := tracker.GetHistory(0)
		assert.NoError(t, err)
		assert.Empty(t, entries)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("defaults to 20 when limit is negative", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		rows := sqlmock.NewRows([]string{
			"history_id", "version", "direction", "status",
			"started_at", "completed_at", "duration_ms",
			"error_message", "applied_by", "created_at",
		})
		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(20).
			WillReturnRows(rows)

		tracker := NewHistoryTracker(db)
		entries, err := tracker.GetHistory(-5)
		assert.NoError(t, err)
		assert.Empty(t, entries)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error on query failure", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		mock.ExpectQuery("SELECT history_id, version, direction, status").
			WithArgs(10).
			WillReturnError(fmt.Errorf("table not found"))

		tracker := NewHistoryTracker(db)
		entries, err := tracker.GetHistory(10)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get history")
		assert.Nil(t, entries)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestStringOrEmpty(t *testing.T) {
	t.Run("returns empty string for nil", func(t *testing.T) {
		assert.Equal(t, "", stringOrEmpty(nil))
	})

	t.Run("returns string value for non-nil", func(t *testing.T) {
		s := "hello"
		assert.Equal(t, "hello", stringOrEmpty(&s))
	})

	t.Run("returns empty string for empty pointer", func(t *testing.T) {
		s := ""
		assert.Equal(t, "", stringOrEmpty(&s))
	})
}
