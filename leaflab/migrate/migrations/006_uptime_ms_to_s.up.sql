-- Rename uptime_ms → uptime_s and store seconds instead of milliseconds.
-- INTEGER is sufficient: uint32 seconds overflows after ~136 years.
-- The processor divides millis() by 1000 before inserting.

ALTER TABLE sensor_reading RENAME COLUMN uptime_ms TO uptime_s;
