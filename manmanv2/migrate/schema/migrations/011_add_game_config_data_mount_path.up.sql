-- Add data_mount_path to game_configs to allow custom volume mount paths in containers
-- (e.g., /data for Minecraft, /config for Valheim)

ALTER TABLE game_configs
ADD COLUMN IF NOT EXISTS data_mount_path TEXT DEFAULT NULL;

COMMENT ON COLUMN game_configs.data_mount_path IS 'Container-side path where the GSC data volume should be mounted. Defaults to /data/game if NULL.';
