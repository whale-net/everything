-- Add nullable hardware address columns to sensor.
-- When i2c_address IS NOT NULL, the (board_id, i2c_address, mux_address, mux_channel)
-- tuple uniquely identifies the physical sensor and is used as the primary lookup key.
-- mux_address / mux_channel are only meaningful when mux_address > 0.

ALTER TABLE sensor
  ADD COLUMN i2c_address SMALLINT,
  ADD COLUMN mux_address SMALLINT,
  ADD COLUMN mux_channel SMALLINT;

-- Partial unique index: enforces hw-address uniqueness only when populated.
CREATE UNIQUE INDEX idx_sensor_hw_address
  ON sensor(board_id, i2c_address, mux_address, mux_channel)
  WHERE i2c_address IS NOT NULL;
