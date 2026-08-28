DROP INDEX IF EXISTS idx_device_config_derived_from_version;
ALTER TABLE device_config DROP CONSTRAINT IF EXISTS fk_device_config_derived_from_version;
ALTER TABLE device_config DROP COLUMN IF EXISTS derived_from_version;
