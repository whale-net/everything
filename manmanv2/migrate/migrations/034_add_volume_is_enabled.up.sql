-- Add is_enabled column to game_config_volumes
ALTER TABLE game_config_volumes
    ADD COLUMN is_enabled BOOLEAN NOT NULL DEFAULT TRUE;
