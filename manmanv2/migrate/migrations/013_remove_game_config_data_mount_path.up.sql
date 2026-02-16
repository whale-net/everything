-- Drop legacy data_mount_path column added in migration 011
ALTER TABLE game_configs DROP COLUMN IF EXISTS data_mount_path;
