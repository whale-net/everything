-- Migration 013: add i2c_address to sensor_hw_history
--
-- The sensor_hw_history table now carries the full canonical hardware key
-- (i2c_address, mux_path, sensor_type), matching FR18's canonical key.
-- The interval itself gains i2c_address; mux_path is already present from migration 009;
-- sensor_type is carried by the sensor row the interval belongs to.
--
-- Backfill policy (NFR8):
-- - Open intervals (valid_to IS NULL) backfill from sensor.i2c_address.
-- - Closed intervals (valid_to IS NOT NULL) backfill to NULL, meaning
--   "not recorded, pre-migration" — never to 0, which is a real I2C address
--   and would fabricate history.
--
-- Indices:
-- - Open-interval partial index: allows O(1) "what is this sensor's current wiring?"
-- - Temporal index: allows efficient queries at a point in time

ALTER TABLE sensor_hw_history ADD COLUMN i2c_address SMALLINT;

-- Backfill: open intervals get current address; closed intervals get NULL.
UPDATE sensor_hw_history shh
SET i2c_address = CASE
    WHEN shh.valid_to IS NULL THEN s.i2c_address
    ELSE NULL
END
FROM sensor s
WHERE s.sensor_id = shh.sensor_id;

-- Drop the old current index (if it exists with a different name after migration 011).
-- We'll recreate it below to ensure it covers both sensor_id and the new i2c_address.
DROP INDEX IF EXISTS idx_sensor_hw_history_current;

-- Create the open-interval partial index: O(1) lookup of current wiring.
CREATE INDEX idx_sensor_hw_history_current
    ON sensor_hw_history(sensor_id, i2c_address) WHERE valid_to IS NULL;

-- Create a temporal index for queries at a point in time:
-- "what wiring was this sensor on at time T?"
CREATE INDEX idx_sensor_hw_history_temporal
    ON sensor_hw_history(sensor_id, valid_from, valid_to);
