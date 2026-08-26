-- Rollback migration 013: remove i2c_address from sensor_hw_history

DROP INDEX IF EXISTS idx_sensor_hw_history_temporal;
DROP INDEX IF EXISTS idx_sensor_hw_history_current;

ALTER TABLE sensor_hw_history DROP COLUMN i2c_address;

-- Recreate the old current index (was renamed in migration 011).
CREATE INDEX idx_sensor_hw_history_current
    ON sensor_hw_history(sensor_id) WHERE valid_to IS NULL;
