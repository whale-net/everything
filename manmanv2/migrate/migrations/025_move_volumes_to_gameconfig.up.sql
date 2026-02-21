-- Create game_config_volumes table (GameConfig-level)
CREATE TABLE IF NOT EXISTS game_config_volumes (
    volume_id      BIGSERIAL PRIMARY KEY,
    config_id      BIGINT NOT NULL REFERENCES game_configs(config_id) ON DELETE CASCADE,
    name           VARCHAR(100) NOT NULL,
    description    TEXT,
    container_path TEXT NOT NULL,
    host_subpath   TEXT,
    read_only      BOOLEAN NOT NULL DEFAULT FALSE,
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(config_id, name)
);
CREATE INDEX idx_game_config_volumes_config_id ON game_config_volumes(config_id);

-- Add volume_id and path_override to configuration_patches
ALTER TABLE configuration_patches
    ADD COLUMN volume_id BIGINT REFERENCES game_config_volumes(volume_id) ON DELETE SET NULL,
    ADD COLUMN path_override TEXT;

-- Migrate existing volume strategies to game_config_volumes
-- For each game with volume strategies, create volume rows for ALL GameConfigs of that game
INSERT INTO game_config_volumes (config_id, name, description, container_path, host_subpath, read_only)
SELECT
    gc.config_id,
    cs.name,
    cs.description,
    COALESCE(cs.target_path, '/data'),  -- container_path from strategy.target_path
    cs.base_template,                   -- host_subpath from strategy.base_template
    COALESCE((cs.render_options->>'read_only')::boolean, false)
FROM configuration_strategies cs
INNER JOIN game_configs gc ON gc.game_id = cs.game_id
WHERE cs.strategy_type = 'volume';

-- Delete volume strategy rows (no longer needed)
DELETE FROM configuration_strategies WHERE strategy_type = 'volume';

-- Remove 'volume' from strategy_type enum
ALTER TABLE configuration_strategies DROP CONSTRAINT configuration_strategies_strategy_type_check;
ALTER TABLE configuration_strategies ADD CONSTRAINT configuration_strategies_strategy_type_check
    CHECK (strategy_type IN (
        'cli_args', 'env_vars',
        'file_properties', 'file_json', 'file_yaml',
        'file_ini', 'file_xml', 'file_lua', 'file_custom'
    ));
