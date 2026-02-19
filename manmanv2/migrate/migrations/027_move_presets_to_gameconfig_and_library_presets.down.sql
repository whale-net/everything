-- Remove overrides from SGC-library attachments
ALTER TABLE sgc_workshop_libraries
    DROP COLUMN IF EXISTS installation_path_override,
    DROP COLUMN IF EXISTS volume_id,
    DROP COLUMN IF EXISTS preset_id;

-- Remove default preset from libraries
ALTER TABLE workshop_libraries
    DROP COLUMN IF EXISTS preset_id;

-- Re-add volume_id to path presets
ALTER TABLE game_addon_path_presets
    ADD COLUMN volume_id BIGINT REFERENCES game_config_volumes(volume_id) ON DELETE SET NULL;
