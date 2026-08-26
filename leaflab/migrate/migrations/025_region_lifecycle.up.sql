-- Migration 025: Region lifecycle -- soft retirement, successor reference,
-- and the parentage-immutability trigger (FR50, FR22.2, NFR6.2)
--
-- Three additions, all scoped to `region`:
--
-- 1. Soft retirement columns, matching the board pattern from migration 018:
--    retired_at IS NULL means active (default); NOT NULL means retired.
--    Each retirement names the operation that set it and the acting principal.
--
-- 2. successor_region_id: set only when a region is retired by relocation
--    (FR74, subtree relocation -- #1223). Lets a region-keyed series join
--    across a reorganisation instead of discontinuing at the retired region.
--    Nullable; NULL means "not retired by relocation" (including "not retired
--    at all").
--
-- 3. The parentage-immutability trigger (NFR6.2): once any reading has been
--    attributed to a region or any of its descendants, that region's
--    parent_region_id can never change again. This is a trigger, not a CHECK
--    constraint -- a CHECK cannot express a recursive descendant test, and
--    the plan says so explicitly (see #1220). Retirement does not unfreeze
--    parentage: the trigger consults sensor_reading regardless of
--    retired_at, and a retired region's parent_region_id is otherwise
--    unchanged by retirement.
--
--    The refusal names the reason and the alternative (FR59.3): subtree
--    relocation, FR74 (#1223). Re-parenting therefore never changes the
--    attribution of any historical reading -- before any reading exists in
--    the subtree, a mis-typed parent can still be corrected (the "create-time
--    grace window" from #1220), since a wired board publishes within one
--    poll interval in practice.

-- -- Add retired state columns to region --------------------------------------

ALTER TABLE region ADD COLUMN retired_at        TIMESTAMPTZ;
ALTER TABLE region ADD COLUMN retired_operation VARCHAR(64);
ALTER TABLE region ADD COLUMN retired_principal VARCHAR(255);

-- successor_region_id: named only when retirement was a relocation (FR74).
-- Self-referential; RESTRICT so a successor can't be deleted out from under
-- retired regions that point to it.
ALTER TABLE region ADD COLUMN successor_region_id BIGINT REFERENCES region(region_id) ON DELETE RESTRICT;

-- NULL retired_at means the region is active (default).
-- Partial index for finding active regions -- default listings exclude retired ones.
CREATE INDEX idx_region_active ON region(region_id) WHERE retired_at IS NULL;

-- -- Parentage-immutability trigger (NFR6.2) -----------------------------------
--
-- Fires BEFORE UPDATE OF parent_region_id, only when the value is actually
-- changing (renames, retirement, and successor-linking never touch
-- parent_region_id and so never invoke this check).
--
-- Walks the *current* tree (as it exists before this update takes effect) to
-- collect the region being re-parented plus every descendant, then checks
-- sensor_reading for any row attributed to that set. sensor_reading.region_id
-- is denormalized at write time (see DATA.md), so this is a direct
-- attribution test, not an inference from current sensor placement.

CREATE OR REPLACE FUNCTION region_freeze_parent_once_attributed()
RETURNS TRIGGER AS $$
DECLARE
    has_reading BOOLEAN;
BEGIN
    -- Only the parentage change itself is guarded; renames, retirement, and
    -- successor-linking pass through untouched.
    IF NEW.parent_region_id IS NOT DISTINCT FROM OLD.parent_region_id THEN
        RETURN NEW;
    END IF;

    SELECT EXISTS (
        WITH RECURSIVE subtree AS (
            SELECT OLD.region_id AS region_id
            UNION ALL
            SELECT r.region_id
            FROM region r
            JOIN subtree s ON r.parent_region_id = s.region_id
        )
        SELECT 1
        FROM sensor_reading sr
        JOIN subtree s ON sr.region_id = s.region_id
    ) INTO has_reading;

    IF has_reading THEN
        RAISE EXCEPTION
            'region % parentage is frozen: a reading has been attributed to this region or a descendant; use subtree relocation (FR74) instead of re-parenting',
            OLD.region_id
            USING ERRCODE = 'check_violation';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER region_freeze_parent_once_attributed
BEFORE UPDATE OF parent_region_id ON region
FOR EACH ROW EXECUTE FUNCTION region_freeze_parent_once_attributed();
