package postgres

import (
	"context"
	"time"

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
			session_id, server_game_config_id, backup_config_id, volume_id,
			s3_url, size_bytes, status, description, created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING backup_id
	`
	err := r.db.QueryRow(ctx, query,
		backup.SessionID,
		backup.ServerGameConfigID,
		backup.BackupConfigID,
		backup.VolumeID,
		backup.S3URL,
		backup.SizeBytes,
		backup.Status,
		backup.Description,
		backup.CreatedAt,
	).Scan(&backup.BackupID)
	return backup, err
}

func (r *BackupRepository) Get(ctx context.Context, backupID int64) (*manman.Backup, error) {
	query := `
		SELECT backup_id, session_id, server_game_config_id, backup_config_id, volume_id,
		       s3_url, size_bytes, status, error_message, description, created_at
		FROM backups WHERE backup_id = $1
	`
	b := &manman.Backup{}
	err := r.db.QueryRow(ctx, query, backupID).Scan(
		&b.BackupID, &b.SessionID, &b.ServerGameConfigID, &b.BackupConfigID, &b.VolumeID,
		&b.S3URL, &b.SizeBytes, &b.Status, &b.ErrorMessage, &b.Description, &b.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func (r *BackupRepository) List(ctx context.Context, sgcID *int64, sessionID *int64, limit int, offset int) ([]*manman.Backup, error) {
	query := `
		SELECT backup_id, session_id, server_game_config_id, backup_config_id, volume_id,
		       s3_url, size_bytes, status, error_message, description, created_at
		FROM backups
		WHERE ($1::bigint IS NULL OR server_game_config_id = $1)
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
		b := &manman.Backup{}
		if err := rows.Scan(
			&b.BackupID, &b.SessionID, &b.ServerGameConfigID, &b.BackupConfigID, &b.VolumeID,
			&b.S3URL, &b.SizeBytes, &b.Status, &b.ErrorMessage, &b.Description, &b.CreatedAt,
		); err != nil {
			return nil, err
		}
		backups = append(backups, b)
	}
	return backups, rows.Err()
}

func (r *BackupRepository) Delete(ctx context.Context, backupID int64) error {
	_, err := r.db.Exec(ctx, `DELETE FROM backups WHERE backup_id = $1`, backupID)
	return err
}

func (r *BackupRepository) UpdateStatus(ctx context.Context, backupID int64, status string, s3URL *string, sizeBytes *int64, errMsg *string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE backups SET status = $2, s3_url = $3, size_bytes = $4, error_message = $5
		WHERE backup_id = $1
	`, backupID, status, s3URL, sizeBytes, errMsg)
	return err
}

// BackupConfigRepository implements repository.BackupConfigRepository
type BackupConfigRepository struct {
	db *pgxpool.Pool
}

func NewBackupConfigRepository(db *pgxpool.Pool) *BackupConfigRepository {
	return &BackupConfigRepository{db: db}
}

func (r *BackupConfigRepository) Create(ctx context.Context, cfg *manman.BackupConfig) (*manman.BackupConfig, error) {
	err := r.db.QueryRow(ctx, `
		INSERT INTO backup_configs (volume_id, cadence_minutes, backup_path, enabled, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NOW(), NOW())
		RETURNING backup_config_id, created_at, updated_at
	`, cfg.VolumeID, cfg.CadenceMinutes, cfg.BackupPath, cfg.Enabled,
	).Scan(&cfg.BackupConfigID, &cfg.CreatedAt, &cfg.UpdatedAt)
	return cfg, err
}

func (r *BackupConfigRepository) Get(ctx context.Context, id int64) (*manman.BackupConfig, error) {
	cfg := &manman.BackupConfig{}
	err := r.db.QueryRow(ctx, `
		SELECT backup_config_id, volume_id, cadence_minutes, backup_path, enabled, last_backup_at, created_at, updated_at
		FROM backup_configs WHERE backup_config_id = $1
	`, id).Scan(
		&cfg.BackupConfigID, &cfg.VolumeID, &cfg.CadenceMinutes, &cfg.BackupPath,
		&cfg.Enabled, &cfg.LastBackupAt, &cfg.CreatedAt, &cfg.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func (r *BackupConfigRepository) List(ctx context.Context, volumeID int64) ([]*manman.BackupConfig, error) {
	rows, err := r.db.Query(ctx, `
		SELECT backup_config_id, volume_id, cadence_minutes, backup_path, enabled, last_backup_at, created_at, updated_at
		FROM backup_configs WHERE volume_id = $1 ORDER BY backup_config_id
	`, volumeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cfgs []*manman.BackupConfig
	for rows.Next() {
		cfg := &manman.BackupConfig{}
		if err := rows.Scan(
			&cfg.BackupConfigID, &cfg.VolumeID, &cfg.CadenceMinutes, &cfg.BackupPath,
			&cfg.Enabled, &cfg.LastBackupAt, &cfg.CreatedAt, &cfg.UpdatedAt,
		); err != nil {
			return nil, err
		}
		cfgs = append(cfgs, cfg)
	}
	return cfgs, rows.Err()
}

func (r *BackupConfigRepository) Update(ctx context.Context, cfg *manman.BackupConfig) error {
	_, err := r.db.Exec(ctx, `
		UPDATE backup_configs
		SET cadence_minutes = $2, backup_path = $3, enabled = $4, updated_at = NOW()
		WHERE backup_config_id = $1
	`, cfg.BackupConfigID, cfg.CadenceMinutes, cfg.BackupPath, cfg.Enabled)
	return err
}

func (r *BackupConfigRepository) Delete(ctx context.Context, id int64) error {
	_, err := r.db.Exec(ctx, `DELETE FROM backup_configs WHERE backup_config_id = $1`, id)
	return err
}

// ListDue returns enabled backup configs whose cadence has elapsed AND whose SGC had an
// active session between last_backup_at (or epoch if null) and now.
func (r *BackupConfigRepository) ListDue(ctx context.Context, now time.Time) ([]*manman.BackupConfig, error) {
	rows, err := r.db.Query(ctx, `
		SELECT DISTINCT bc.backup_config_id, bc.volume_id, bc.cadence_minutes, bc.backup_path,
		                bc.enabled, bc.last_backup_at, bc.created_at, bc.updated_at
		FROM backup_configs bc
		JOIN game_config_volumes gcv ON gcv.volume_id = bc.volume_id
		JOIN game_configs gc ON gc.config_id = gcv.config_id
		JOIN server_game_configs sgc ON sgc.game_config_id = gc.config_id
		JOIN sessions s ON s.sgc_id = sgc.sgc_id
		WHERE bc.enabled = true
		  -- cadence has elapsed since last backup (or never backed up)
		  AND (
		      bc.last_backup_at IS NULL
		      OR bc.last_backup_at + (bc.cadence_minutes * INTERVAL '1 minute') <= $1
		  )
		  -- SGC had a session active at any point since last backup:
		  -- still running (ended_at IS NULL) or ended after last_backup_at
		  AND (
		      s.ended_at IS NULL
		      OR s.ended_at >= COALESCE(bc.last_backup_at, 'epoch'::timestamp)
		  )
	`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cfgs []*manman.BackupConfig
	for rows.Next() {
		cfg := &manman.BackupConfig{}
		if err := rows.Scan(
			&cfg.BackupConfigID, &cfg.VolumeID, &cfg.CadenceMinutes, &cfg.BackupPath,
			&cfg.Enabled, &cfg.LastBackupAt, &cfg.CreatedAt, &cfg.UpdatedAt,
		); err != nil {
			return nil, err
		}
		cfgs = append(cfgs, cfg)
	}
	return cfgs, rows.Err()
}

func (r *BackupConfigRepository) UpdateLastBackupAt(ctx context.Context, id int64, t time.Time) error {
	_, err := r.db.Exec(ctx, `
		UPDATE backup_configs SET last_backup_at = $2, updated_at = NOW() WHERE backup_config_id = $1
	`, id, t)
	return err
}

func (r *BackupConfigRepository) AddAction(ctx context.Context, backupConfigID, actionID int64, displayOrder int) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO backup_config_actions (backup_config_id, action_id, display_order)
		VALUES ($1, $2, $3) ON CONFLICT (backup_config_id, action_id) DO UPDATE SET display_order = $3
	`, backupConfigID, actionID, displayOrder)
	return err
}

func (r *BackupConfigRepository) RemoveAction(ctx context.Context, backupConfigID, actionID int64) error {
	_, err := r.db.Exec(ctx, `
		DELETE FROM backup_config_actions WHERE backup_config_id = $1 AND action_id = $2
	`, backupConfigID, actionID)
	return err
}

func (r *BackupConfigRepository) ListActions(ctx context.Context, backupConfigID int64) ([]*manman.BackupConfigAction, error) {
	rows, err := r.db.Query(ctx, `
		SELECT backup_config_id, action_id, display_order
		FROM backup_config_actions WHERE backup_config_id = $1 ORDER BY display_order
	`, backupConfigID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var actions []*manman.BackupConfigAction
	for rows.Next() {
		a := &manman.BackupConfigAction{}
		if err := rows.Scan(&a.BackupConfigID, &a.ActionID, &a.DisplayOrder); err != nil {
			return nil, err
		}
		actions = append(actions, a)
	}
	return actions, rows.Err()
}
