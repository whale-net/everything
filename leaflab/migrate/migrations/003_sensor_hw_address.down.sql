DROP INDEX IF EXISTS idx_sensor_hw_address;

ALTER TABLE sensor
  DROP COLUMN IF EXISTS i2c_address,
  DROP COLUMN IF EXISTS mux_address,
  DROP COLUMN IF EXISTS mux_channel;
