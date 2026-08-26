-- Remove provenance tracking from device_config.
ALTER TABLE device_config DROP COLUMN provenance_json;
