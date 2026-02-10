package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/manman"
)

type LogReferenceRepository struct {
	db *pgxpool.Pool
}

func NewLogReferenceRepository(db *pgxpool.Pool) *LogReferenceRepository {
	return &LogReferenceRepository{db: db}
}

func (r *LogReferenceRepository) Create(ctx context.Context, logRef *manman.LogReference) error {
	query := `
		INSERT INTO log_references (
			session_id, sgc_id, file_path, start_time, end_time,
			line_count, source, minute_timestamp, state, appended_at, created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING log_id
	`

	err := r.db.QueryRow(ctx, query,
		logRef.SessionID,
		logRef.SGCID,
		logRef.FilePath,
		logRef.StartTime,
		logRef.EndTime,
		logRef.LineCount,
		logRef.Source,
		logRef.MinuteTimestamp,
		logRef.State,
		logRef.AppendedAt,
		logRef.CreatedAt,
	).Scan(&logRef.LogID)

	return err
}

func (r *LogReferenceRepository) ListBySession(ctx context.Context, sessionID int64) ([]*manman.LogReference, error) {
	query := `
		SELECT log_id, session_id, sgc_id, file_path, start_time, end_time,
		       line_count, source, minute_timestamp, state, appended_at, created_at
		FROM log_references
		WHERE session_id = $1
		ORDER BY start_time
	`

	rows, err := r.db.Query(ctx, query, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logRefs []*manman.LogReference
	for rows.Next() {
		logRef := &manman.LogReference{}
		err := rows.Scan(
			&logRef.LogID,
			&logRef.SessionID,
			&logRef.SGCID,
			&logRef.FilePath,
			&logRef.StartTime,
			&logRef.EndTime,
			&logRef.LineCount,
			&logRef.Source,
			&logRef.MinuteTimestamp,
			&logRef.State,
			&logRef.AppendedAt,
			&logRef.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		logRefs = append(logRefs, logRef)
	}

	return logRefs, rows.Err()
}

func (r *LogReferenceRepository) GetByMinute(ctx context.Context, sgcID int64, minuteTimestamp time.Time) (*manman.LogReference, error) {
	query := `
		SELECT log_id, session_id, sgc_id, file_path, start_time, end_time,
		       line_count, source, minute_timestamp, state, appended_at, created_at
		FROM log_references
		WHERE sgc_id = $1 AND minute_timestamp = $2
		ORDER BY created_at DESC
		LIMIT 1
	`

	logRef := &manman.LogReference{}
	err := r.db.QueryRow(ctx, query, sgcID, minuteTimestamp).Scan(
		&logRef.LogID,
		&logRef.SessionID,
		&logRef.SGCID,
		&logRef.FilePath,
		&logRef.StartTime,
		&logRef.EndTime,
		&logRef.LineCount,
		&logRef.Source,
		&logRef.MinuteTimestamp,
		&logRef.State,
		&logRef.AppendedAt,
		&logRef.CreatedAt,
	)

	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return logRef, nil
}

func (r *LogReferenceRepository) UpdateState(ctx context.Context, logID int64, state string) error {
	query := `
		UPDATE log_references
		SET state = $2
		WHERE log_id = $1
	`

	_, err := r.db.Exec(ctx, query, logID, state)
	return err
}

func (r *LogReferenceRepository) ListByTimeRange(ctx context.Context, sgcID int64, startTime, endTime time.Time) ([]*manman.LogReference, error) {
	query := `
		SELECT log_id, session_id, sgc_id, file_path, start_time, end_time,
		       line_count, source, minute_timestamp, state, appended_at, created_at
		FROM log_references
		WHERE sgc_id = $1
		  AND minute_timestamp >= $2
		  AND minute_timestamp <= $3
		  AND state = 'complete'
		ORDER BY minute_timestamp ASC
	`

	rows, err := r.db.Query(ctx, query, sgcID, startTime, endTime)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logRefs []*manman.LogReference
	for rows.Next() {
		logRef := &manman.LogReference{}
		err := rows.Scan(
			&logRef.LogID,
			&logRef.SessionID,
			&logRef.SGCID,
			&logRef.FilePath,
			&logRef.StartTime,
			&logRef.EndTime,
			&logRef.LineCount,
			&logRef.Source,
			&logRef.MinuteTimestamp,
			&logRef.State,
			&logRef.AppendedAt,
			&logRef.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		logRefs = append(logRefs, logRef)
	}

	return logRefs, rows.Err()
}

func (r *LogReferenceRepository) GetMinMaxTimes(ctx context.Context, sgcID int64) (minTime, maxTime *time.Time, err error) {
	query := `
		SELECT MIN(minute_timestamp), MAX(minute_timestamp)
		FROM log_references
		WHERE sgc_id = $1 AND state = 'complete'
	`

	err = r.db.QueryRow(ctx, query, sgcID).Scan(&minTime, &maxTime)
	if err != nil {
		return nil, nil, err
	}

	return minTime, maxTime, nil
}
