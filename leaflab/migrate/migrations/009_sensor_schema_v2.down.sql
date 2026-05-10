-- Reverse migration 009

-- 4.
ALTER TABLE sensor_reading DROP COLUMN IF EXISTS config_version;

-- 3. Restore sensor_hw_history scalar columns from first hop only.
ALTER TABLE sensor_hw_history
    ADD COLUMN mux_address SMALLINT,
    ADD COLUMN mux_channel SMALLINT;

UPDATE sensor_hw_history
SET mux_address = (mux_path->0->>'muxAddress')::smallint,
    mux_channel = (mux_path->0->>'muxChannel')::smallint
WHERE jsonb_array_length(mux_path) > 0;

ALTER TABLE sensor_hw_history DROP COLUMN mux_path;

-- 2. Restore sensor scalar columns from first hop only.
ALTER TABLE sensor
    ADD COLUMN mux_address SMALLINT,
    ADD COLUMN mux_channel SMALLINT;

UPDATE sensor
SET mux_address = (mux_path->0->>'muxAddress')::smallint,
    mux_channel = (mux_path->0->>'muxChannel')::smallint
WHERE jsonb_array_length(mux_path) > 0;

DROP INDEX IF EXISTS idx_sensor_hw_address;

CREATE UNIQUE INDEX idx_sensor_hw_address
    ON sensor(board_id, i2c_address, mux_address, mux_channel, sensor_type_id)
    WHERE i2c_address IS NOT NULL;

ALTER TABLE sensor DROP COLUMN mux_path;

-- 1.
DROP TABLE IF EXISTS sensor_label;
