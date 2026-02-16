-- Backups table for game save data
CREATE TABLE IF NOT EXISTS backups (
    backup_id BIGSERIAL PRIMARY KEY,
    session_id BIGINT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
    server_game_config_id BIGINT NOT NULL REFERENCES server_game_configs(sgc_id) ON DELETE CASCADE,
    s3_url TEXT NOT NULL,  -- S3 URL for the backup tarball (e.g., s3://bucket/backups/sgc_id/backup_id.tar.gz)
    size_bytes BIGINT NOT NULL,
    description TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Indexes for backup queries
CREATE INDEX IF NOT EXISTS idx_backups_session_id ON backups(session_id);
CREATE INDEX IF NOT EXISTS idx_backups_sgc_id ON backups(server_game_config_id);
CREATE INDEX IF NOT EXISTS idx_backups_created_at ON backups(created_at DESC);

-- Add backup_id reference to sessions for tracking restore source
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS restored_from_backup_id BIGINT REFERENCES backups(backup_id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_sessions_restored_from ON sessions(restored_from_backup_id);

COMMENT ON TABLE backups IS 'Compressed backups of game save data stored in S3';
COMMENT ON COLUMN backups.s3_url IS 'S3 URL for the backup tarball (format: s3://bucket/backups/{sgc_id}/{backup_id}.tar.gz)';
COMMENT ON COLUMN sessions.restored_from_backup_id IS 'Backup ID that was used to restore this session''s data (if any)';
