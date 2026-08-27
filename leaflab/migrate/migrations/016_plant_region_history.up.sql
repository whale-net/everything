-- Migration 016: plant_region_history schema (Phase 3 scaffold, FR19/FR21)
--
-- Picked 016 as the next free number after 015 (ownership); state in the PR
-- since sibling v2 branches on plan/1166 have collided on migration numbers
-- before.
--
-- This is the schema-only half of defect 1's fix: plant, plant_type and
-- v_sensor_reading_with_plant have existed since migration 001 and nothing
-- writes them -- the view joins p.region_id = e.region_id (current
-- placement, exact equality), so moving a plant re-attributes every reading
-- it ever produced.
--
-- plant_region_history is the SCD2 record of truth for where a plant has
-- been over time, following AGENTS.md's valid_from/valid_to convention.
-- Nothing is backfilled here -- the attribution-neutral, snapped-to-hour
-- backfill (FR21) and the no-back-dating writer/guard (FR19) land in this
-- task's Implementation phase, ordered before FR25/FR28/FR72 per NFR8.
-- plant.region_id is left in place (not dropped or repurposed) -- see the
-- Implementation-phase migration for the disposition decision.

CREATE TABLE plant_region_history (
    plant_region_history_id BIGSERIAL PRIMARY KEY,
    plant_id           BIGINT NOT NULL REFERENCES plant(plant_id),
    region_id          BIGINT NOT NULL REFERENCES region(region_id),
    valid_from          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to            TIMESTAMPTZ,
    relocation_induced  BOOLEAN NOT NULL DEFAULT FALSE  -- FR24, written from Phase 5
);

-- Open-interval partial indexes, both directions (NFR6.1): plant-to-region
-- at T and region-to-plant at T are both hot paths for FR20's read path.
CREATE INDEX idx_plant_region_history_plant_id_current
    ON plant_region_history(plant_id) WHERE valid_to IS NULL;
CREATE INDEX idx_plant_region_history_region_id_current
    ON plant_region_history(region_id) WHERE valid_to IS NULL;

-- Temporal indexes serving AGENTS.md's value-at-time-T predicate, both
-- directions.
CREATE INDEX idx_plant_region_history_plant_id_temporal
    ON plant_region_history(plant_id, valid_from, valid_to);
CREATE INDEX idx_plant_region_history_region_id_temporal
    ON plant_region_history(region_id, valid_from, valid_to);
