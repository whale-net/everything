-- Remove is_enabled column from game_config_volumes
ALTER TABLE game_config_volumes
    DROP COLUMN is_enabled;
