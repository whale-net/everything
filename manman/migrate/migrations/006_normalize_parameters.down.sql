-- Rollback normalized parameter schema

-- Drop views first
DROP VIEW IF EXISTS parameter_value_distribution CASCADE;
DROP VIEW IF EXISTS configs_with_missing_required_params CASCADE;
DROP VIEW IF EXISTS parameter_usage_stats CASCADE;

-- Drop tables
DROP TABLE IF EXISTS session_parameter_values CASCADE;
DROP TABLE IF EXISTS server_game_config_parameter_values CASCADE;
DROP TABLE IF EXISTS game_config_parameter_values CASCADE;
DROP TABLE IF EXISTS parameter_definitions CASCADE;
