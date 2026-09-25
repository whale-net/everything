ALTER TABLE workshop_addons ALTER COLUMN preset_id DROP NOT NULL;

ALTER TABLE workshop_addons ADD COLUMN IF NOT EXISTS volume_id BIGINT REFERENCES game_config_volumes(volume_id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_workshop_addons_volume_id ON workshop_addons(volume_id);
