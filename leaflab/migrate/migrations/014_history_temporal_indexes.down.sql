-- Reverse migration 014: drop the two temporal indexes added above. The
-- pre-existing partial indexes (idx_sensor_name_history_current,
-- idx_sensor_region_history_current) predate this migration and are left
-- untouched.

DROP INDEX IF EXISTS idx_sensor_region_history_temporal;
DROP INDEX IF EXISTS idx_sensor_name_history_temporal;
