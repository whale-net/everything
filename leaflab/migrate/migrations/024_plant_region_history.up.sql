-- Migration 024: plant_region_history SCD2 table for placement history
--
-- Plant placement is now historical and immutable. A plant's placement in a
-- region is recorded as SCD2 with valid_from / valid_to (AGENTS.md — no synonyms).
--
-- A move closes the current interval and opens a new one; nothing is updated
-- in place. Placement boundaries are never back-dated: an interval opens at the
-- instant the change is recorded.
--
-- This migration creates the schema and backfills from plant.region_id with
-- outward snapping applied at migration time only. No reading changes its
-- attribution as a result of this migration.

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

-- ── Backfill from plant.region_id with hourly bucket snapping ───────────────
--
-- Backfilling preserves attribution neutrality (no reading changes attribution):
-- - Each plant's initial interval uses its current region_id
-- - Opening boundary snaps to start of hourly bucket containing earliest reading
--   in that region (or plant.created_at if no readings exist)
-- - For removed plants, closing boundary snaps to end of hourly bucket containing
--   removal time
-- - Active plants leave valid_to NULL (open interval)
--
-- Snapping to hourly boundaries guarantees:
-- - No straddling bucket at any timestamp (all boundaries on hourly edges)
-- - Bounded cost: a removed plant may share its final hour with its successor,
--   marked per FR26.2

WITH region_earliest_readings AS (
    -- Find the earliest reading timestamp for each region (by any sensor).
    -- This establishes the lower bound for plant intervals in that region.
    SELECT
        sr.region_id,
        MIN(sr.recorded_at) AS earliest_reading
    FROM sensor_reading sr
    WHERE sr.region_id IS NOT NULL
    GROUP BY sr.region_id
),
backfilled_intervals AS (
    -- Compute the snapped placement intervals from current plant state.
    -- For each plant, determine:
    -- - Opening boundary: start of hour containing earliest reading in region, or
    --   start of hour containing plant creation if no readings exist
    -- - Closing boundary: end of hour containing removal, or NULL if still active
    SELECT
        p.plant_id,
        p.region_id,
        DATE_TRUNC('hour', COALESCE(rer.earliest_reading, p.created_at)) AS snapped_valid_from,
        CASE
            WHEN p.removed_at IS NOT NULL
            THEN DATE_TRUNC('hour', p.removed_at) + INTERVAL '1 hour' - INTERVAL '1 microsecond'
            ELSE NULL
        END AS snapped_valid_to
    FROM plant p
    LEFT JOIN region_earliest_readings rer ON rer.region_id = p.region_id
)
INSERT INTO plant_region_history (plant_id, region_id, valid_from, valid_to)
SELECT plant_id, region_id, snapped_valid_from, snapped_valid_to
FROM backfilled_intervals;
