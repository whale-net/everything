-- Reverse migration 011: restore original SCD2 table/column names.

-- ── 3. sensor_hw_history ─────────────────────────────────────────────────────

DROP INDEX idx_sensor_hw_history_current;
CREATE INDEX idx_sensor_hw_history_current
    ON sensor_hw_history(sensor_id) WHERE unassigned_at IS NULL;

ALTER TABLE sensor_hw_history
    RENAME COLUMN valid_from TO assigned_at;
ALTER TABLE sensor_hw_history
    RENAME COLUMN valid_to   TO unassigned_at;

-- ── 2. sensor_region_history ──────────────────────────────────────────────────

DROP INDEX idx_sensor_region_history_current;
CREATE INDEX idx_sensor_region_history_current
    ON sensor_region_history(sensor_id) WHERE unassigned_at IS NULL;

ALTER TABLE sensor_region_history
    RENAME COLUMN valid_from TO assigned_at;
ALTER TABLE sensor_region_history
    RENAME COLUMN valid_to   TO unassigned_at;

-- ── 1. sensor_name_history → sensor_label ────────────────────────────────────

ALTER INDEX idx_sensor_name_history_current   RENAME TO idx_sensor_label_current;
ALTER INDEX idx_sensor_name_history_sensor_id RENAME TO idx_sensor_label_sensor_id;

ALTER TABLE sensor_name_history RENAME TO sensor_label;
