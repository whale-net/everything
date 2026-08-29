-- Migration 020: region lifecycle -- soft retirement, successor reference,
-- and the parentage-immutability trigger (Phase 5 scaffold: FR50, FR22.2,
-- FR22.5, NFR6.2, part of #1376).
--
-- 020 is the next free number after 019 (attribution); 013 and 018 are
-- independently claimed by sibling v2 branches not (yet) merged into this
-- lineage -- see 015_ownership's doc comment for the same numbering
-- collision note. Gaps in the sequence are expected and tolerated by
-- golang-migrate (leaflab/migrate/main.go); nothing here depends on
-- contiguous numbering.

-- ── region.retired_at / region.successor_region_id (FR22.2, FR22.5) ────────
--
-- retired_at: NULL means still active/reporting. Set by RetireRegion
-- (Implementation phase, leaflab/api/regions.go); mirrors board.retired_at
-- (migration 015) -- a retired region is excluded from default listings,
-- accepts no new writes, and remains readable by explicit id and through
-- the history/readings paths. Per FR22.2 it must also remain resolvable
-- for attribution of readings recorded while it was active -- the Phase 3
-- attribution resolver (attribute_region_plants, migration 019) walks
-- v_region_path's path_ids regardless of retired_at, so retirement here
-- does not by itself change what that resolver returns; Implementation
-- must assert this rather than assume it.
--
-- successor_region_id: populated only when a region is retired *by a
-- relocation* (FR74, a sibling task) -- naming the region that replaced it
-- so region-keyed series can follow the reorganisation instead of
-- discontinuing (FR22.2). NULL for every other retirement reason. Nothing
-- in this schema enforces "set only alongside a relocation-flavoured
-- retirement" -- that pairing is an API-level (FR74) invariant, not a
-- database one, same as plant_region_history.relocation_induced
-- (migration 017) is API-set rather than schema-derived.
ALTER TABLE region ADD COLUMN retired_at          TIMESTAMPTZ;
ALTER TABLE region ADD COLUMN successor_region_id BIGINT REFERENCES region(region_id) ON DELETE RESTRICT;

-- Partial index for the default (non-retired) listing population, mirroring
-- idx_board_active (migration 015).
CREATE INDEX idx_region_active ON region(region_id) WHERE retired_at IS NULL;

-- ── v_region_household ──────────────────────────────────────────────────────
--
-- Resolves every region's household by walking to its tree root -- only the
-- root carries region.household_id directly (migration 015's
-- enforce_region_household_root trigger; descendants carry NULL and
-- inherit). leaflab/api/regions.go's household-scoped read paths
-- (authz.Scope.Filter, FR5.1/FR5.2, mirroring ListBoards) must select FROM
-- this view alone -- never joined alongside the raw `region` table in the
-- same query. `region` itself carries a (mostly NULL, root-only)
-- household_id column, so a query with both `region` and this view in its
-- FROM clause would make an unqualified `household_id = $N` filter fragment
-- ambiguous; Scope.Filter's contract assumes exactly one such column is in
-- scope. This view therefore carries every column a region read path needs
-- (region_id, parent_region_id, name, description, retired_at,
-- successor_region_id) precisely so no caller ever has to join it back
-- against `region` for the rest.
CREATE VIEW v_region_household AS
SELECT
    r.region_id,
    r.parent_region_id,
    r.name,
    r.description,
    r.retired_at,
    r.successor_region_id,
    root.household_id
FROM region r
JOIN v_region_path rp ON rp.region_id = r.region_id
JOIN region root      ON root.region_id = rp.path_ids[1];

-- ── Parentage-immutability trigger (FR50.3, FR22.2, NFR6.2) ────────────────
--
-- FR50.3: parentage is set at create and is immutable once any reading has
-- been attributed to that region or any of its descendants. Since a wired
-- board publishes within one poll interval, this is in practice a
-- create-time grace window for a mis-typed parent, not a user-facing
-- re-parenting capability. FR22.2: retirement does not unfreeze parentage
-- -- this trigger fires on any parent_region_id UPDATE regardless of
-- retired_at, so a retired region is exactly as frozen as an active one.
--
-- Implemented as a trigger, not a CHECK: a CHECK constraint cannot express
-- "walk this region's descendants and check a sibling table for rows" --
-- CHECK bodies must be a single row-local, IMMUTABLE-safe expression, never
-- a subquery over other rows (NFR6.2 names this explicitly: "a trigger
-- running a recursive descendant test ... not a CHECK constraint, which
-- cannot express it"). The recursive CTE below walks region.parent_region_id
-- directly rather than reusing v_region_path, which materialises root-to-
-- leaf paths for every region and is a heavier read than the single-subtree
-- walk this trigger needs on every parent_region_id UPDATE.
--
-- leaflab/api/regions.go's RenameRegion/RetireRegion (Implementation phase)
-- add the FR59.3 caller-facing refusal -- naming FR74/subtree relocation as
-- the alternative -- in front of this trigger, so a caller sees a
-- structured refusal rather than a raw database error; this trigger is the
-- backstop that holds even for a direct SQL UPDATE that bypasses the API
-- (NFR6.2), which is the case the Testing phase must exercise explicitly.
CREATE FUNCTION enforce_region_parentage_immutable() RETURNS TRIGGER AS $$
DECLARE
    v_has_reading BOOLEAN;
BEGIN
    -- OF parent_region_id already restricts firing to updates that
    -- reference the column in their SET clause; this guard additionally
    -- skips a SET parent_region_id = <same value> no-op, so only a genuine
    -- change pays for the recursive descendant test below.
    IF NEW.parent_region_id IS NOT DISTINCT FROM OLD.parent_region_id THEN
        RETURN NEW;
    END IF;

    WITH RECURSIVE subtree AS (
        SELECT region_id FROM region WHERE region_id = OLD.region_id

        UNION ALL

        SELECT r.region_id
        FROM region r
        JOIN subtree s ON r.parent_region_id = s.region_id
    )
    SELECT EXISTS (
        SELECT 1 FROM sensor_reading sr WHERE sr.region_id IN (SELECT region_id FROM subtree)
    ) INTO v_has_reading;

    IF v_has_reading THEN
        RAISE EXCEPTION 'region % parentage is frozen: a reading has been attributed to it or a descendant region (FR50.3, NFR6.2); relocate the subtree instead (FR74)',
            OLD.region_id;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_region_parentage_immutable
    BEFORE UPDATE OF parent_region_id ON region
    FOR EACH ROW
    EXECUTE FUNCTION enforce_region_parentage_immutable();
