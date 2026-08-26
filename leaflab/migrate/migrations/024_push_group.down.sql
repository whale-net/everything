DROP INDEX IF EXISTS idx_push_group_membership_group;
DROP TABLE IF EXISTS push_group_membership;

DROP INDEX IF EXISTS idx_device_config_push_group;
ALTER TABLE device_config DROP COLUMN IF EXISTS push_group_id;

DROP INDEX IF EXISTS idx_push_group_created;
DROP TABLE IF EXISTS push_group;
