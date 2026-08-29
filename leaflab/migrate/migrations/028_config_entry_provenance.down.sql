-- Reverse migration 028: drop per-entry config provenance.

DROP INDEX IF EXISTS idx_device_config_entry_hw_key;
DROP INDEX IF EXISTS idx_device_config_entry_config_id;
DROP TABLE IF EXISTS device_config_entry;
