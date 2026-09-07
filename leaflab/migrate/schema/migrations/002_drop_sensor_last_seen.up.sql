-- Remove last_seen_at from sensor.
-- Last activity is derivable from the most recent sensor_reading row.

ALTER TABLE sensor DROP COLUMN last_seen_at;
