-- Migration 015 down: reverse the ownership schema.
-- (NOT NULL on plant.household_id and the backfilled values need no explicit
-- reversal -- dropping the column below removes both.)

ALTER TABLE plant DROP COLUMN household_id;

DROP TRIGGER IF EXISTS trg_region_household_root ON region;
DROP FUNCTION IF EXISTS enforce_region_household_root();

ALTER TABLE region DROP COLUMN household_id;

ALTER TABLE board DROP COLUMN retired_at;
ALTER TABLE board DROP COLUMN household_id;

DROP TRIGGER IF EXISTS trg_board_ownership_no_unadopted_arrivals ON board_ownership;
DROP FUNCTION IF EXISTS enforce_no_unadopted_arrivals();

DROP TABLE IF EXISTS board_ownership;
DROP TABLE IF EXISTS household_membership;
DROP TABLE IF EXISTS household;
