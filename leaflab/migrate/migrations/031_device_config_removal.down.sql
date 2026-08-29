-- Reverse migration 031: drop FR82.6's config-removal bookkeeping.

DROP INDEX IF EXISTS idx_device_config_removal_config_id;
DROP TABLE IF EXISTS device_config_removal;
