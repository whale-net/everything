package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/manmanv2"
)

type BackupRepository struct {
	db *pgxpool.Pool
}

func NewBackupRepository(db *pgxpool.Pool) *BackupRepository {
	return &BackupRepository{db: db}
}

func (r *BackupRepository) Create(ctx context.Context, backup *manman.Backup) (*manman.Backup, error) {
	query := `
		INSERT INTO backups (
			session_id, server_game_config_id, s3_url, size_bytes, description, created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING backup_id
	`

	err := r.db.QueryRow(ctx, query,
		backup.SessionID,
		backup.ServerGameConfigID,
		backup.S3URL,
		backup.SizeBytes,
		backup.Description,
		backup.CreatedAt,
	).Scan(&backup.BackupID)

	return backup, err
}

func (r *BackupRepository) Get(ctx context.Context, backupID int64) (*manman.Backup, error) {
	query := `
		SELECT backup_id, session_id, server_game_config_id, s3_url, size_bytes, description, created_at
		FROM backups
		WHERE backup_id = $1
	`

	backup := &manman.Backup{}
	err := r.db.QueryRow(ctx, query, backupID).Scan(
		&backup.BackupID,
		&backup.SessionID,
		&backup.ServerGameConfigID,
		&backup.S3URL,
		&backup.SizeBytes,
		&backup.Description,
		&backup.CreatedAt,
	)

	if err != nil {
		return nil, err
	}

	return backup, nil
}

func (r *BackupRepository) List(ctx context.Context, sgcID *int64, sessionID *int64, limit int, offset int) ([]*manman.Backup, error) {
	query := `
		SELECT backup_id, session_id, server_game_config_id, s3_url, size_bytes, description, created_at
		FROM backups
		WHERE
			($1::bigint IS NULL OR server_game_config_id = $1)
			AND ($2::bigint IS NULL OR session_id = $2)
		ORDER BY created_at DESC
		LIMIT $3 OFFSET $4
	`

	rows, err := r.db.Query(ctx, query, sgcID, sessionID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var backups []*manman.Backup
	for rows.Next() {
		backup := &manman.Backup{}
		err := rows.Scan(
			&backup.BackupID,
			&backup.SessionID,
			&backup.ServerGameConfigID,
			&backup.S3URL,
			&backup.SizeBytes,
			&backup.Description,
			&backup.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		backups = append(backups, backup)
	}

	return backups, rows.Err()
}

func (r *BackupRepository) Delete(ctx context.Context, backupID int64) error {
	query := `DELETE FROM backups WHERE backup_id = $1`
	_, err := r.db.Exec(ctx, query, backupID)
	return err
}
