-- Migration 013: sensor_hw_history gains i2c_address, with NULL-not-zero backfill
--
-- sensor_hw_history currently carries only mux_path (migration 009 dropped
-- mux_address/mux_channel, added mux_path, but never added an i2c_address
-- column) -- it cannot record an address change at all. The canonical
-- hardware key (FR18, see //leaflab/hwkey) is
-- (i2c_address, mux_path, sensor_type); the sensor_type component is carried
-- by the sensor row the interval belongs to (sensor_id), so this interval
-- table gains i2c_address alongside its existing mux_path (FR16.1).
--
-- Recording only (i2c_address, mux_path) would not uniquely identify the row
-- whose history the interval closes -- one SHT3x is two sensor rows at one
-- address -- and would not match the key FR82.4 removes an entry by. That's
-- why the sensor_id foreign key (already present) plus the new i2c_address
-- together with the sensor row's sensor_type_id give the full three-component
-- key for any given interval.

ALTER TABLE sensor_hw_history ADD COLUMN i2c_address SMALLINT;

-- FR16.2 backfill, in exactly two statements so the distinction is auditable.

-- 1. Open intervals: backfill i2c_address from the sensor's current address.
UPDATE sensor_hw_history h
SET i2c_address = s.i2c_address
FROM sensor s
WHERE s.sensor_id = h.sensor_id
  AND h.valid_to IS NULL;

-- 2. Closed intervals deliberately left NULL: "not recorded, pre-migration".
-- Never write 0 here -- 0 is a real, present I2C address (the
-- //leaflab/hwkey AddressOpt "unknown address" sentinel, distinct from
-- absent), and writing it to a closed interval whose address was never
-- actually recorded would fabricate history that never happened (NFR8).
-- No statement is needed for this step: the column defaults to NULL and
-- step 1 only touches open intervals.

-- NFR6.1: sensor_hw_history needs both an open-interval partial index and a
-- temporal index. The open-interval partial index
-- (idx_sensor_hw_history_current) already exists from migrations 005/011;
-- add the missing temporal index.
CREATE INDEX idx_sensor_hw_history_temporal
    ON sensor_hw_history(sensor_id, valid_from, valid_to);
