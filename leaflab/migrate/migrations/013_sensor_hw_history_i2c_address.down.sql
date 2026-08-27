-- Reverse migration 013: drop the added temporal index and i2c_address column.
-- The open-interval partial index (idx_sensor_hw_history_current) predates
-- this migration and is left untouched.

DROP INDEX IF EXISTS idx_sensor_hw_history_temporal;

ALTER TABLE sensor_hw_history DROP COLUMN IF EXISTS i2c_address;
