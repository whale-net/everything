DROP TRIGGER IF EXISTS trg_device_config_append_only ON device_config;
DROP FUNCTION IF EXISTS enforce_device_config_append_only();
DROP INDEX IF EXISTS idx_device_config_derived_from_version;
ALTER TABLE device_config DROP CONSTRAINT IF EXISTS fk_device_config_derived_from_version;
ALTER TABLE device_config DROP COLUMN IF EXISTS derived_from_version;
