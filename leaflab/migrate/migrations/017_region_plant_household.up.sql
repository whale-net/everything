-- Migration 017: Add household_id to region and plant
--
-- Adds household_id column to region (tree roots only; descendants inherit)
-- and to plant. Nullable during migration; implementation phase will populate
-- and add constraints as needed.

-- ── Add household_id to region ───────────────────────────────────────────────
-- Only root regions (parent_region_id IS NULL) carry household_id.
-- Descendants inherit through tree traversal (implementation/application logic).

ALTER TABLE region ADD COLUMN household_id BIGINT REFERENCES household(household_id) ON DELETE RESTRICT;

-- ── Add household_id to plant ────────────────────────────────────────────────

ALTER TABLE plant ADD COLUMN household_id BIGINT REFERENCES household(household_id) ON DELETE RESTRICT;
