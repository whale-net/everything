-- Migration 011: SCD2 naming consistency
--
-- 1. Rename sensor_label → sensor_name_history (matches the _history suffix on
--    the other two SCD2 tables).
-- 2. Rename assigned_at/unassigned_at → valid_from/valid_to on
--    sensor_region_history and sensor_hw_history (canonical SCD2 column names;
--    sensor_name_history already uses valid_from/valid_to).
--
-- All renames are metadata-only — no data is rewritten.

-- ── 1. sensor_label → sensor_name_history ────────────────────────────────────

ALTER TABLE sensor_label RENAME TO sensor_name_history;

ALTER INDEX idx_sensor_label_sensor_id RENAME TO idx_sensor_name_history_sensor_id;
ALTER INDEX idx_sensor_label_current   RENAME TO idx_sensor_name_history_current;

-- ── 2. sensor_region_history: assigned_at/unassigned_at → valid_from/valid_to ─

ALTER TABLE sensor_region_history
    RENAME COLUMN assigned_at   TO valid_from;
ALTER TABLE sensor_region_history
    RENAME COLUMN unassigned_at TO valid_to;

ALTER INDEX idx_sensor_region_history_current
    RENAME TO idx_sensor_region_history_current_pre_rename;

-- Recreate partial index with new column name so the index expression is correct.
DROP INDEX idx_sensor_region_history_current_pre_rename;
CREATE INDEX idx_sensor_region_history_current
    ON sensor_region_history(sensor_id) WHERE valid_to IS NULL;

-- ── 3. sensor_hw_history: assigned_at/unassigned_at → valid_from/valid_to ─────

ALTER TABLE sensor_hw_history
    RENAME COLUMN assigned_at   TO valid_from;
ALTER TABLE sensor_hw_history
    RENAME COLUMN unassigned_at TO valid_to;

ALTER INDEX idx_sensor_hw_history_current
    RENAME TO idx_sensor_hw_history_current_pre_rename;

DROP INDEX idx_sensor_hw_history_current_pre_rename;
CREATE INDEX idx_sensor_hw_history_current
    ON sensor_hw_history(sensor_id) WHERE valid_to IS NULL;
