-- Migration 034: plant_type ownership (Phase 5 scaffold, FR55)
--
-- 034 is the next free migration number: the highest present anywhere on
-- disk across every .pm-worktrees/* checkout at scaffold time was 033
-- (033_boundary_capture, the #1360/#1362/#1377/#1379 lineage) -- 034 was
-- unclaimed anywhere. Stating this in the PR since sibling v2 branches on
-- this plan have collided on migration numbers before (#1439's
-- 022_tiers/019_attribution collision, #1452's dual 034 claim by #1371 and
-- #1380) -- if this collides with another sibling branch by the time this
-- merges, that is an integration-time renumbering, per those scope notes'
-- precedent, not a defect in this migration itself.
--
-- Adds the plant-type catalog's ownership split (FR55, A24): a plant_type
-- row is either global (household_id IS NULL, readable by any authenticated
-- principal, writable only under FR10 elevation) or household-owned
-- (household_id set, created/renamed/retired by a member-or-grantee (FR7)
-- of that household -- no elevation). retired_at is plant_type's own soft
-- retirement column (FR22.1): nothing is hard-deleted, mirroring
-- plant.removed_at / region.retired_at / board.retired_at.

ALTER TABLE plant_type ADD COLUMN household_id BIGINT REFERENCES household(household_id) ON DELETE RESTRICT;
ALTER TABLE plant_type ADD COLUMN retired_at   TIMESTAMPTZ;

-- Default-listing population (active plant types), scoped by ownership --
-- mirrors idx_board_active/idx_plant_active's "WHERE <active predicate>"
-- convention. Not unique/singleton: a household may own several plant
-- types, and several households (or the global class) may each hold a
-- same-named type -- FR55's "two same-named types are distinguishable"
-- case relies on that being allowed.
CREATE INDEX idx_plant_type_household_id ON plant_type(household_id) WHERE retired_at IS NULL;
