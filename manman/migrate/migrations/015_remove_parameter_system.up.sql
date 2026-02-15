-- Remove parameter system tables in favor of patches-only approach
-- We're simplifying to use configuration_patches for all value overrides

-- Drop parameter-related views
DROP VIEW IF EXISTS parameter_usage_stats;
DROP VIEW IF EXISTS configs_with_missing_required_params;
DROP VIEW IF EXISTS parameter_value_distribution;

-- Drop normalized parameter tables
DROP TABLE IF EXISTS session_parameter_values CASCADE;
DROP TABLE IF EXISTS server_game_config_parameter_values CASCADE;
DROP TABLE IF EXISTS game_config_parameter_values CASCADE;
DROP TABLE IF EXISTS strategy_parameter_bindings CASCADE;
DROP TABLE IF EXISTS parameter_definitions CASCADE;

-- Drop JSONB parameter columns from original tables
ALTER TABLE game_configs DROP COLUMN IF EXISTS parameters;
ALTER TABLE server_game_configs DROP COLUMN IF EXISTS parameters;
ALTER TABLE sessions DROP COLUMN IF EXISTS parameters;
