DROP INDEX IF EXISTS idx_device_config_push_group_id;
ALTER TABLE device_config DROP COLUMN IF EXISTS push_group_id;
DROP TABLE IF EXISTS push_group;
