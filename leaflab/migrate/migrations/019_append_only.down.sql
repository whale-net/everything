-- Down migration 019: Remove append-only enforcement

DROP TRIGGER IF EXISTS trigger_device_config_append_only ON device_config;
DROP FUNCTION IF EXISTS enforce_append_only_device_config();
