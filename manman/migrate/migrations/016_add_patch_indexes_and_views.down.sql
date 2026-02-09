-- Drop views and their triggers
DROP TRIGGER IF EXISTS session_patches_delete_trigger ON session_patches;
DROP TRIGGER IF EXISTS session_patches_update_trigger ON session_patches;
DROP TRIGGER IF EXISTS session_patches_insert_trigger ON session_patches;
DROP FUNCTION IF EXISTS session_patches_delete();
DROP FUNCTION IF EXISTS session_patches_update();
DROP FUNCTION IF EXISTS session_patches_insert();

DROP TRIGGER IF EXISTS server_game_config_patches_delete_trigger ON server_game_config_patches;
DROP TRIGGER IF EXISTS server_game_config_patches_update_trigger ON server_game_config_patches;
DROP TRIGGER IF EXISTS server_game_config_patches_insert_trigger ON server_game_config_patches;
DROP FUNCTION IF EXISTS server_game_config_patches_delete();
DROP FUNCTION IF EXISTS server_game_config_patches_update();
DROP FUNCTION IF EXISTS server_game_config_patches_insert();

DROP TRIGGER IF EXISTS game_config_patches_delete_trigger ON game_config_patches;
DROP TRIGGER IF EXISTS game_config_patches_update_trigger ON game_config_patches;
DROP TRIGGER IF EXISTS game_config_patches_insert_trigger ON game_config_patches;
DROP FUNCTION IF EXISTS game_config_patches_delete();
DROP FUNCTION IF EXISTS game_config_patches_update();
DROP FUNCTION IF EXISTS game_config_patches_insert();

DROP VIEW IF EXISTS session_patches;
DROP VIEW IF EXISTS server_game_config_patches;
DROP VIEW IF EXISTS game_config_patches;

-- Drop covering indexes
DROP INDEX IF EXISTS idx_config_patches_strategy_level_covering;
DROP INDEX IF EXISTS idx_config_patches_level_entity_covering;
