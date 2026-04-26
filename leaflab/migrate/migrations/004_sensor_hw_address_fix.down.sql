DROP INDEX IF EXISTS idx_sensor_hw_address;

CREATE UNIQUE INDEX idx_sensor_hw_address
  ON sensor(board_id, i2c_address, mux_address, mux_channel)
  WHERE i2c_address IS NOT NULL;
