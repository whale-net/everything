-- Migration 014: NFR6.1 index pair completeness for the two remaining
-- SCD2 history tables
--
-- NFR6.1 requires every SCD2 history table to carry both an open-interval
-- partial index (fast "what's current?") and a temporal index (fast
-- "what was true at time T?" / ordered scans for FR53's timeline reads).
-- sensor_hw_history got its temporal index in migration 013
-- (idx_sensor_hw_history_temporal), alongside its pre-existing partial
-- index. sensor_name_history and sensor_region_history each still only
-- have the partial index (idx_sensor_name_history_current /
-- idx_sensor_region_history_current, both from migrations 009/001+011) --
-- this migration adds the missing temporal index to both, so FR53's three
-- timelines are backed by the same index shape.

CREATE INDEX idx_sensor_name_history_temporal
    ON sensor_name_history(sensor_id, valid_from, valid_to);

CREATE INDEX idx_sensor_region_history_temporal
    ON sensor_region_history(sensor_id, valid_from, valid_to);
