-- Fix hw-address unique index to include sensor_type_id.
-- Without it, two sensors on the same physical chip (e.g. SHT3x temperature +
-- humidity, or CCS811 eCO2 + TVOC) would collide because they share the same
-- i2c_address/mux_address/mux_channel but produce different measurement types.

DROP INDEX IF EXISTS idx_sensor_hw_address;

CREATE UNIQUE INDEX idx_sensor_hw_address
  ON sensor(board_id, i2c_address, mux_address, mux_channel, sensor_type_id)
  WHERE i2c_address IS NOT NULL;
