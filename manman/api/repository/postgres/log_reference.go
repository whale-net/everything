package postgres

import (
	"context"

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
			session_id, file_path, start_time, end_time,
			line_count, source, created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING log_id
	`

	err := r.db.QueryRow(ctx, query,
		logRef.SessionID,
		logRef.FilePath,
		logRef.StartTime,
		logRef.EndTime,
		logRef.LineCount,
		logRef.Source,
		logRef.CreatedAt,
	).Scan(&logRef.LogID)

	return err
}

func (r *LogReferenceRepository) ListBySession(ctx context.Context, sessionID int64) ([]*manman.LogReference, error) {
	query := `
		SELECT log_id, session_id, file_path, start_time, end_time,
		       line_count, source, created_at
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
			&logRef.FilePath,
			&logRef.StartTime,
			&logRef.EndTime,
			&logRef.LineCount,
			&logRef.Source,
			&logRef.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		logRefs = append(logRefs, logRef)
	}

	return logRefs, rows.Err()
}
