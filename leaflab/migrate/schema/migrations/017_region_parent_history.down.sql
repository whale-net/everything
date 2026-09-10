-- Reverse migration 017: drop board_region_history, then board.region_id and
-- its index, then region_parent_history, in reverse order of the up
-- migration. No data preservation is expected on a down migration here.

DROP TABLE IF EXISTS board_region_history;

DROP INDEX IF EXISTS idx_board_region_id;
ALTER TABLE board DROP COLUMN IF EXISTS region_id;

DROP TABLE IF EXISTS region_parent_history;
