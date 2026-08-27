-- Migration 017 down: reverse the plant_region_history schema, backfill and
-- guard. The backfilled rows need no separate reversal -- DROP TABLE
-- removes them along with the schema. plant.region_id is untouched (this
-- migration never dropped or altered it -- see the up.sql cache-vs-drop
-- decision).

DROP TRIGGER IF EXISTS trg_plant_region_history_no_future_valid_from ON plant_region_history;
DROP FUNCTION IF EXISTS enforce_plant_region_history_no_future_valid_from();

DROP TABLE IF EXISTS plant_region_history;
