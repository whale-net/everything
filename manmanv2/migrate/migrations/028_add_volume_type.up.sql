-- Add volume_type column to game_config_volumes
ALTER TABLE game_config_volumes
    ADD COLUMN volume_type VARCHAR(20) NOT NULL DEFAULT 'bind'
    CHECK (volume_type IN ('bind', 'named'));
