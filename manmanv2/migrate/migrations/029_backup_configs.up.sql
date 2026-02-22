-- BackupConfig: many per GameConfigVolume, defines scheduled backup behavior
CREATE TABLE IF NOT EXISTS backup_configs (
    backup_config_id BIGSERIAL PRIMARY KEY,
    volume_id        BIGINT  NOT NULL REFERENCES game_config_volumes(volume_id) ON DELETE CASCADE,
    cadence_minutes  INT     NOT NULL CHECK (cadence_minutes > 0),
    backup_path      TEXT    NOT NULL, -- relative path within the volume to back up (e.g. "saves" or ".")
    enabled          BOOLEAN NOT NULL DEFAULT true,
    last_backup_at   TIMESTAMP,
    created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_backup_configs_volume_id ON backup_configs(volume_id);

-- Ordered pre-backup actions to execute before archiving
CREATE TABLE IF NOT EXISTS backup_config_actions (
    backup_config_id BIGINT NOT NULL REFERENCES backup_configs(backup_config_id) ON DELETE CASCADE,
    action_id        BIGINT NOT NULL REFERENCES action_definitions(action_id) ON DELETE CASCADE,
    display_order    INT    NOT NULL DEFAULT 0,
    PRIMARY KEY (backup_config_id, action_id)
);

-- Extend backups table with config/volume linkage and status
ALTER TABLE backups
    ADD COLUMN IF NOT EXISTS backup_config_id BIGINT REFERENCES backup_configs(backup_config_id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS volume_id        BIGINT REFERENCES game_config_volumes(volume_id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS status           TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'running', 'completed', 'failed')),
    ADD COLUMN IF NOT EXISTS error_message    TEXT;

-- s3_url and size_bytes are unknown until completion
ALTER TABLE backups
    ALTER COLUMN s3_url     DROP NOT NULL,
    ALTER COLUMN size_bytes DROP NOT NULL;

CREATE INDEX IF NOT EXISTS idx_backups_backup_config_id ON backups(backup_config_id);
CREATE INDEX IF NOT EXISTS idx_backups_status ON backups(status);
