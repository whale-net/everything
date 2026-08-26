-- Rollback: drop plant_region_history and its indexes

DROP INDEX IF EXISTS idx_plant_region_history_region_temporal;
DROP INDEX IF EXISTS idx_plant_region_history_plant_temporal;
DROP INDEX IF EXISTS idx_plant_region_history_region_current;
DROP INDEX IF EXISTS idx_plant_region_history_plant_current;
DROP TABLE IF EXISTS plant_region_history;
