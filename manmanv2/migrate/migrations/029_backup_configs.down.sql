DROP TABLE IF EXISTS backup_config_actions;
DROP TABLE IF EXISTS backup_configs;

ALTER TABLE backups
    DROP COLUMN IF EXISTS backup_config_id,
    DROP COLUMN IF EXISTS volume_id,
    DROP COLUMN IF EXISTS status,
    DROP COLUMN IF EXISTS error_message;

ALTER TABLE backups
    ALTER COLUMN s3_url     SET NOT NULL,
    ALTER COLUMN size_bytes SET NOT NULL;
