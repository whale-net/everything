-- Audit trail of sensor wiring changes (mux channel / i2c address).
-- Mirrors sensor_region_history for physical placement.
-- unassigned_at = NULL means this is the current wiring.

CREATE TABLE sensor_hw_history (
    history_id    BIGSERIAL PRIMARY KEY,
    sensor_id     BIGINT   NOT NULL REFERENCES sensor(sensor_id) ON DELETE RESTRICT,
    mux_address   SMALLINT,
    mux_channel   SMALLINT,
    assigned_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    unassigned_at TIMESTAMPTZ
);

CREATE INDEX idx_sensor_hw_history_sensor_id ON sensor_hw_history(sensor_id);
CREATE INDEX idx_sensor_hw_history_current   ON sensor_hw_history(sensor_id) WHERE unassigned_at IS NULL;

-- Backfill current wiring from sensor table.
-- Use registered_at as assigned_at — best approximation of when they were first wired.
INSERT INTO sensor_hw_history (sensor_id, mux_address, mux_channel, assigned_at)
SELECT sensor_id, mux_address, mux_channel, registered_at
FROM sensor
WHERE mux_address IS NOT NULL OR mux_channel IS NOT NULL;
