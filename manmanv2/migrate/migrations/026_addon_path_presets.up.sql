-- Create game_addon_path_presets table for reusable installation path templates
CREATE TABLE IF NOT EXISTS game_addon_path_presets (
    preset_id         BIGSERIAL PRIMARY KEY,
    game_id           BIGINT NOT NULL REFERENCES games(game_id) ON DELETE CASCADE,
    name              VARCHAR(100) NOT NULL,
    description       TEXT,
    volume_id         BIGINT REFERENCES game_config_volumes(volume_id) ON DELETE SET NULL,
    installation_path TEXT NOT NULL,
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(game_id, name)
);

CREATE INDEX idx_game_addon_path_presets_game_id ON game_addon_path_presets(game_id);
CREATE INDEX idx_game_addon_path_presets_volume_id ON game_addon_path_presets(volume_id);

-- Add preset_id and volume_id to workshop_addons for flexible path configuration
ALTER TABLE workshop_addons
    ADD COLUMN preset_id BIGINT REFERENCES game_addon_path_presets(preset_id) ON DELETE SET NULL,
    ADD COLUMN volume_id BIGINT REFERENCES game_config_volumes(volume_id) ON DELETE SET NULL;

CREATE INDEX idx_workshop_addons_preset_id ON workshop_addons(preset_id);
CREATE INDEX idx_workshop_addons_volume_id ON workshop_addons(volume_id);

-- Migration note:
-- Presets allow users to define common installation paths per game (e.g., "Maps", "Plugins")
-- Addons can reference a preset OR specify custom volume_id + installation_path
-- Path resolution priority: preset_id > (volume_id + installation_path) > first volume fallback
