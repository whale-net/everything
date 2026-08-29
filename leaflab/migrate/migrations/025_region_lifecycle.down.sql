-- Migration 020 down: reverse the parentage-immutability trigger,
-- v_region_household, and region's retired_at/successor_region_id columns.

DROP TRIGGER IF EXISTS trg_region_parentage_immutable ON region;
DROP FUNCTION IF EXISTS enforce_region_parentage_immutable();

DROP VIEW IF EXISTS v_region_household;

DROP INDEX IF EXISTS idx_region_active;

ALTER TABLE region DROP COLUMN successor_region_id;
ALTER TABLE region DROP COLUMN retired_at;
