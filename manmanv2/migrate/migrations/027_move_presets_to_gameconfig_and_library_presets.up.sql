-- Remove volume_id from path presets (volume selection should happen at usage time, not preset definition)
ALTER TABLE game_addon_path_presets DROP COLUMN IF EXISTS volume_id;

-- Add default preset to workshop libraries
-- This is the default installation path preset for all addons in the library
ALTER TABLE workshop_libraries
    ADD COLUMN preset_id BIGINT REFERENCES game_addon_path_presets(preset_id) ON DELETE SET NULL;

-- Add preset_id, volume_id, and path override to SGC-library attachments
-- This allows overriding the library's default preset, volume, or using a completely custom path
ALTER TABLE sgc_workshop_libraries
    ADD COLUMN preset_id BIGINT REFERENCES game_addon_path_presets(preset_id) ON DELETE SET NULL,
    ADD COLUMN volume_id BIGINT REFERENCES game_config_volumes(volume_id) ON DELETE SET NULL,
    ADD COLUMN installation_path_override TEXT;
