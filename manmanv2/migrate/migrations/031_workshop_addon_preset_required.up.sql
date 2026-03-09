-- Remove volume_id from workshop_addons: volumes are SGC-scoped, not addon-scoped.
-- Volume overrides belong on sgc_workshop_libraries (attachment level).
DROP INDEX IF EXISTS idx_workshop_addons_volume_id;
ALTER TABLE workshop_addons DROP COLUMN IF EXISTS volume_id;

-- Make preset_id NOT NULL: every addon must have a path preset.
ALTER TABLE workshop_addons ALTER COLUMN preset_id SET NOT NULL;
