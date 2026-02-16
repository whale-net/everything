-- Remove parameter system tables in favor of patches-only approach
-- We're simplifying to use configuration_patches for all value overrides

DROP TABLE IF EXISTS session_parameter_values CASCADE;
DROP TABLE IF EXISTS server_game_config_parameter_values CASCADE;
DROP TABLE IF EXISTS game_config_parameter_values CASCADE;
DROP TABLE IF EXISTS strategy_parameter_bindings CASCADE;
DROP TABLE IF EXISTS parameter_definitions CASCADE;
