-- Migration 034 down: drop sensor_region_history.relocation_induced.

ALTER TABLE sensor_region_history
    DROP COLUMN relocation_induced;
