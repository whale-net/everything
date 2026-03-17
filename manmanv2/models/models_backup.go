package manman

import "time"

// Backup represents a backup of game save data for a session
type Backup struct {
	BackupID           int64     `db:"backup_id"`
	SessionID          int64     `db:"session_id"`
	ServerGameConfigID int64     `db:"server_game_config_id"`
	BackupConfigID     *int64    `db:"backup_config_id"` // nil for manual backups
	VolumeID           *int64    `db:"volume_id"`
	S3URL              *string   `db:"s3_url"`         // set on completion
	SizeBytes          *int64    `db:"size_bytes"`     // set on completion
	Status             string    `db:"status"`         // pending/running/completed/failed
	ErrorMessage       *string   `db:"error_message"`
	Description        *string   `db:"description"`
	CreatedAt          time.Time `db:"created_at"`
}

// BackupConfig defines a scheduled backup for a specific volume
type BackupConfig struct {
	BackupConfigID int64      `db:"backup_config_id"`
	VolumeID       int64      `db:"volume_id"`
	CadenceMinutes int        `db:"cadence_minutes"`
	BackupPath     string     `db:"backup_path"` // relative path within volume
	Enabled        bool       `db:"enabled"`
	LastBackupAt   *time.Time `db:"last_backup_at"`
	CreatedAt      time.Time  `db:"created_at"`
	UpdatedAt      time.Time  `db:"updated_at"`
}

// BackupConfigAction is an ordered pre-backup action for a BackupConfig
type BackupConfigAction struct {
	BackupConfigID int64 `db:"backup_config_id"`
	ActionID       int64 `db:"action_id"`
	DisplayOrder   int   `db:"display_order"`
}
