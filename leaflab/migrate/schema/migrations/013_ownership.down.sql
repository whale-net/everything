-- Reverse migration 013: drop ownership columns/indexes, then
-- board_owner_history, then leaflab_user, in FK-safe order.

DROP INDEX IF EXISTS idx_plant_owner;
DROP INDEX IF EXISTS idx_region_owner;

ALTER TABLE plant  DROP COLUMN IF EXISTS owner_leaflab_user_id;
ALTER TABLE region DROP COLUMN IF EXISTS owner_leaflab_user_id;

DROP TABLE IF EXISTS board_owner_history;

DROP TABLE IF EXISTS leaflab_user;
