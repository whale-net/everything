-- Migration 020: Backfill ownership to Unadopted household
--
-- Assigns all pre-existing boards, regions, plants, sensors, and readings to
-- the member-less Unadopted household. Post-condition: zero rows resolve to no
-- household (FR70). After this migration, Unadopted takes no new arrivals.

-- Get Unadopted household_id for use in this migration.
DO $$
DECLARE
    unadopted_id BIGINT;
BEGIN
    SELECT household_id INTO unadopted_id
    FROM household WHERE name = 'Unadopted'
    LIMIT 1;

    IF unadopted_id IS NULL THEN
        RAISE EXCEPTION 'Unadopted household not found; run 014_household first';
    END IF;

    -- ── Backfill board.household_id and board_ownership ──────────────────────────
    -- Assign all existing boards to Unadopted, and create history rows.
    INSERT INTO board_ownership (board_id, household_id, valid_from, valid_to)
    SELECT board_id, unadopted_id, registered_at, NULL
    FROM board
    ON CONFLICT DO NOTHING;

    -- Set household_id on all boards (current value).
    UPDATE board SET household_id = unadopted_id
    WHERE household_id IS NULL;

    -- ── Backfill region.household_id ────────────────────────────────────────────
    -- Assign all root regions (parent_region_id IS NULL) to Unadopted.
    -- Descendants inherit through tree traversal; no household_id needed on them.
    UPDATE region SET household_id = unadopted_id
    WHERE parent_region_id IS NULL AND household_id IS NULL;

    -- ── Backfill plant.household_id ─────────────────────────────────────────────
    -- Assign all plants to Unadopted.
    UPDATE plant SET household_id = unadopted_id
    WHERE household_id IS NULL;

END $$;
