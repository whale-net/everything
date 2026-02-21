-- Reverse: Add 'volume' back to enum
ALTER TABLE configuration_strategies DROP CONSTRAINT configuration_strategies_strategy_type_check;
ALTER TABLE configuration_strategies ADD CONSTRAINT configuration_strategies_strategy_type_check
    CHECK (strategy_type IN (
        'cli_args', 'env_vars', 'file_properties', 'file_json',
        'file_yaml', 'file_ini', 'file_xml', 'file_lua', 'file_custom',
        'volume'
    ));

-- Recreate volume strategies from game_config_volumes (best effort, may lose data)
INSERT INTO configuration_strategies (game_id, name, description, strategy_type, target_path, base_template, render_options)
SELECT DISTINCT
    gc.game_id,
    gcv.name,
    gcv.description,
    'volume',
    gcv.container_path,
    gcv.host_subpath,
    jsonb_build_object('read_only', gcv.read_only)
FROM game_config_volumes gcv
INNER JOIN game_configs gc ON gc.config_id = gcv.config_id;

-- Drop columns from patches
ALTER TABLE configuration_patches DROP COLUMN IF EXISTS path_override;
ALTER TABLE configuration_patches DROP COLUMN IF EXISTS volume_id;

-- Drop table
DROP INDEX IF EXISTS idx_game_config_volumes_config_id;
DROP TABLE IF EXISTS game_config_volumes;
