-- Remove volume_type column from game_config_volumes
ALTER TABLE game_config_volumes
    DROP COLUMN volume_type;
