-- Re-add data_mount_path to game_configs if rolling back migration 013
ALTER TABLE game_configs
ADD COLUMN IF NOT EXISTS data_mount_path TEXT DEFAULT NULL;

COMMENT ON COLUMN game_configs.data_mount_path IS 'Container-side path where the GSC data volume should be mounted. Defaults to /data/game if NULL.';
