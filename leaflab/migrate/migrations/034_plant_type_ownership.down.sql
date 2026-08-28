-- Migration 034 down: reverse the plant_type ownership schema.

DROP INDEX IF EXISTS idx_plant_type_household_id;
ALTER TABLE plant_type DROP COLUMN IF EXISTS retired_at;
ALTER TABLE plant_type DROP COLUMN IF EXISTS household_id;
