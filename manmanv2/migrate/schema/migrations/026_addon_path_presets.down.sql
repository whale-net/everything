-- Drop indexes first
DROP INDEX IF EXISTS idx_workshop_addons_volume_id;
DROP INDEX IF EXISTS idx_workshop_addons_preset_id;
DROP INDEX IF EXISTS idx_game_addon_path_presets_volume_id;
DROP INDEX IF EXISTS idx_game_addon_path_presets_game_id;

-- Remove columns from workshop_addons
ALTER TABLE workshop_addons DROP COLUMN IF EXISTS volume_id;
ALTER TABLE workshop_addons DROP COLUMN IF EXISTS preset_id;

-- Drop presets table
DROP TABLE IF EXISTS game_addon_path_presets;
