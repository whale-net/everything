-- Migration 025: Plant retired state and relocation-induced placement marker
--
-- FR22.5 — a plant's retired state names the operation that sets it, records
-- the acting principal (FR8), and names the guard: excluded from default
-- listings, accepts no new writes, still readable by explicit id and through
-- the history and readings paths (FR22.3). Mirrors the board pattern from
-- migration 018.
--
-- plant.removed_at already exists (migration 001) and continues to be the
-- retired-state timestamp per the "acquired / removed" vocabulary this domain
-- already uses; NULL means active, non-NULL means retired. This migration
-- only adds the columns 001 lacks: which operation retired the plant and who
-- (FR8) performed it.
--
-- FR24 — a placement change caused by a subtree relocation (FR74, #1223) is
-- marked as relocation-induced on plant_region_history, so a plant's timeline
-- distinguishes "this plant moved" from "the region this plant was in moved".

-- ── Add retired-state columns to plant ──────────────────────────────────────

ALTER TABLE plant ADD COLUMN retired_operation VARCHAR(64);
ALTER TABLE plant ADD COLUMN retired_principal VARCHAR(255);

-- plant.removed_at IS NULL means active (default); NOT NULL means retired.
-- idx_plant_active (migration 001) already partial-indexes on
-- (region_id) WHERE removed_at IS NULL for "active plants in a region"
-- lookups, which is the guard's "excluded from default listings" query shape.

-- ── Add relocation-induced marker to plant_region_history ──────────────────

ALTER TABLE plant_region_history ADD COLUMN relocation_induced BOOLEAN NOT NULL DEFAULT FALSE;
