-- Migration 009: sensor schema v2
--
-- 1. sensor_label: versioned name history (sensor.name stays as denormalised
--    current-value cache; source of truth for display queries is sensor_label)
-- 2. sensor.mux_path JSONB: replaces scalar mux_address/mux_channel, supports
--    multi-hop cascaded mux chains
-- 3. sensor_hw_history: same mux_path JSONB migration
-- 4. sensor_reading.config_version: records the accepted config that was active
--    when the reading was taken (NULL = no config ever pushed at that time)

-- ── 1. sensor_label ──────────────────────────────────────────────────────────
-- valid_to IS NULL = current label for that sensor.
-- sensor.name is kept in sync by the processor as a denormalised copy.

CREATE TABLE sensor_label (
    sensor_label_id BIGSERIAL   PRIMARY KEY,
    sensor_id       BIGINT      NOT NULL REFERENCES sensor(sensor_id) ON DELETE CASCADE,
    name            TEXT        NOT NULL,
    valid_from      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to        TIMESTAMPTZ,
    UNIQUE (sensor_id, valid_from)
);

CREATE INDEX idx_sensor_label_sensor_id ON sensor_label(sensor_id);
-- Partial index makes "what is this sensor called right now?" O(1).
CREATE INDEX idx_sensor_label_current   ON sensor_label(sensor_id) WHERE valid_to IS NULL;

-- Backfill one open label per existing sensor, starting at registered_at.
INSERT INTO sensor_label (sensor_id, name, valid_from)
SELECT sensor_id, name, registered_at
FROM sensor;

-- ── 2. sensor.mux_path JSONB ──────────────────────────────────────────────────
-- Format: [{"muxAddress":112,"muxChannel":6}, ...] ordered outer→inner.
-- Empty array means sensor is directly on the root I2C bus.

ALTER TABLE sensor ADD COLUMN mux_path JSONB NOT NULL DEFAULT '[]'::jsonb;

-- Backfill: existing single-hop rows → one-element array.
UPDATE sensor
SET mux_path = CASE
    WHEN mux_address IS NOT NULL
    THEN jsonb_build_array(
             jsonb_build_object(
                 'muxAddress', mux_address::int,
                 'muxChannel', mux_channel::int
             )
         )
    ELSE '[]'::jsonb
END;

-- Replace old partial unique index with one keyed on mux_path::text.
-- JSONB→text is deterministic (object keys are sorted; array order is preserved)
-- so equality via cast is safe.
DROP INDEX IF EXISTS idx_sensor_hw_address;

CREATE UNIQUE INDEX idx_sensor_hw_address
    ON sensor(board_id, i2c_address, sensor_type_id, (mux_path::text))
    WHERE i2c_address IS NOT NULL;

ALTER TABLE sensor
    DROP COLUMN mux_address,
    DROP COLUMN mux_channel;

-- ── 3. sensor_hw_history: migrate to mux_path JSONB ─────────────────────────

ALTER TABLE sensor_hw_history ADD COLUMN mux_path JSONB NOT NULL DEFAULT '[]'::jsonb;

UPDATE sensor_hw_history
SET mux_path = CASE
    WHEN mux_address IS NOT NULL
    THEN jsonb_build_array(
             jsonb_build_object(
                 'muxAddress', mux_address::int,
                 'muxChannel', mux_channel::int
             )
         )
    ELSE '[]'::jsonb
END;

ALTER TABLE sensor_hw_history
    DROP COLUMN mux_address,
    DROP COLUMN mux_channel;

-- ── 4. sensor_reading.config_version ─────────────────────────────────────────

ALTER TABLE sensor_reading ADD COLUMN config_version BIGINT;
