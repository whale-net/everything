-- Migration 024: plant_region_history SCD2 table for placement history
--
-- Plant placement is now historical and immutable. A plant's placement in a
-- region is recorded as SCD2 with valid_from / valid_to (AGENTS.md — no synonyms).
--
-- A move closes the current interval and opens a new one; nothing is updated
-- in place. Placement boundaries are never back-dated: an interval opens at the
-- instant the change is recorded.
--
-- This migration creates the empty schema; #1205 backfills it from plant.region_id
-- with outward snapping applied at migration time only. No reading changes its
-- attribution as a result of this schema creation.

-- ── plant_region_history: SCD2 table with both index shapes ─────────────────

CREATE TABLE plant_region_history (
    plant_id       BIGINT NOT NULL REFERENCES plant(plant_id) ON DELETE RESTRICT,
    region_id      BIGINT NOT NULL REFERENCES region(region_id) ON DELETE RESTRICT,
    valid_from     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to       TIMESTAMPTZ,
    PRIMARY KEY (plant_id, valid_from)
);

-- Open-interval partial indexes for "current placement" lookups (hot path for FR20).
-- These are scanned for a single plant/region to find the current (valid_to IS NULL) row.

CREATE INDEX idx_plant_region_history_plant_current
    ON plant_region_history(plant_id) WHERE valid_to IS NULL;

CREATE INDEX idx_plant_region_history_region_current
    ON plant_region_history(region_id) WHERE valid_to IS NULL;

-- Temporal indexes serving "value at time T" predicates (both plant and region are hot paths).
-- Used by v_sensor_reading_with_plant (fixed in #1206) to join plants active at
-- recorded_at without snapping assignments to their current position.
--
-- The predicate is:
--   WHERE plant_id = $1
--     AND valid_from <= $t
--     AND (valid_to IS NULL OR valid_to > $t)
--
-- We index both dimensions to support both lookups:
-- - "plant at time T" (FR20: reading attribution; FR26: audit trail)
-- - "all plants in region at time T" (FR28: dashboard rollup; FR72: billing snapshot)

CREATE INDEX idx_plant_region_history_plant_temporal
    ON plant_region_history(plant_id, valid_from DESC, valid_to DESC);

CREATE INDEX idx_plant_region_history_region_temporal
    ON plant_region_history(region_id, valid_from DESC, valid_to DESC);
