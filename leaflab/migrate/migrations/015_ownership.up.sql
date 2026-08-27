-- Migration 015: ownership schema (Phase 2 scaffold)
--
-- Picked 015 as the next free number after 014 (htmxauth_sessions); state in
-- the PR since sibling v2 branches on plan/1166 have collided on migration
-- numbers before (013 was independently claimed twice).
--
-- Ownership is rooted at the household, not the board (FR1.1, A1): a region
-- subtree can hold sensors from several boards, so per-board ACLs cannot
-- express "my regions" or "my plants". household_id is carried on board, on
-- the region tree root (descendants inherit), and on plant; sensors and
-- readings inherit through their board.
--
-- Carries both the schema (tables, columns, indexes) and the DML that seeds
-- the Unadopted household and backfills every pre-existing board, region
-- root and plant into it (FR70.1, NFR8), plus the new-arrivals guard (A9).
-- The board retirement operation itself (RetireBoard) lives in
-- leaflab/api/repository.go -- retired_at and its partial index are the only
-- schema this migration owns for it.

-- ── household ────────────────────────────────────────────────────────────────
-- is_unadopted marks the single seeded, member-less household that FR70.1's
-- backfill assigns every pre-existing row to. A9: it receives no new arrivals
-- after migration -- enforced below by trg_board_ownership_no_unadopted_arrivals,
-- once board_ownership exists and this migration's own backfill has run.

CREATE TABLE household (
    household_id BIGSERIAL PRIMARY KEY,
    name         TEXT NOT NULL,
    is_unadopted BOOLEAN NOT NULL DEFAULT FALSE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- At most one Unadopted household can ever exist.
CREATE UNIQUE INDEX idx_household_unadopted_singleton
    ON household(is_unadopted) WHERE is_unadopted = TRUE;

-- Seed the single Unadopted household (FR70.1). No membership rows -- A9: it
-- receives no new arrivals after migration, enforced below once
-- board_ownership exists to guard.
INSERT INTO household (name, is_unadopted) VALUES ('Unadopted', TRUE);

-- ── household_membership ────────────────────────────────────────────────────
-- Principal (person) to household. SCD2 per NFR6.1/NFR6.3 -- membership is
-- history-bearing. Write path is AGENTS.md's close-and-open pattern.

CREATE TABLE household_membership (
    household_membership_id BIGSERIAL PRIMARY KEY,
    household_id            BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
    principal_subject       TEXT NOT NULL,
    valid_from               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to                 TIMESTAMPTZ
);

-- Open-interval partial indexes (current-value lookups).
CREATE INDEX idx_household_membership_household_id_current
    ON household_membership(household_id) WHERE valid_to IS NULL;
CREATE INDEX idx_household_membership_principal_subject_current
    ON household_membership(principal_subject) WHERE valid_to IS NULL;
-- Temporal index serving AGENTS.md's value-at-time-T predicate.
CREATE INDEX idx_household_membership_household_id_temporal
    ON household_membership(household_id, valid_from, valid_to);

-- ── board_ownership ──────────────────────────────────────────────────────────
-- Board to household, SCD2. Tracks board re-ownership history (e.g. reclaim);
-- board.household_id below is kept in sync as the current-value cache.

CREATE TABLE board_ownership (
    board_ownership_id BIGSERIAL PRIMARY KEY,
    board_id            BIGINT NOT NULL REFERENCES board(board_id) ON DELETE RESTRICT,
    household_id        BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
    valid_from           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to             TIMESTAMPTZ
);

-- Open-interval partial index (current-value lookup).
CREATE INDEX idx_board_ownership_board_id_current
    ON board_ownership(board_id) WHERE valid_to IS NULL;
-- Temporal index serving AGENTS.md's value-at-time-T predicate.
CREATE INDEX idx_board_ownership_board_id_temporal
    ON board_ownership(board_id, valid_from, valid_to);

-- ── board.household_id / board.retired_at ───────────────────────────────────
-- household_id is nullable: an unclaimed self-registered board resolves to no
-- household until claimed (FR1.1's one exception, reached only through FR76's
-- claim path and the admin's elevated lane).
-- retired_at is set by a named retirement operation (Implementation phase);
-- NULL means still in the reporting population.

ALTER TABLE board ADD COLUMN household_id BIGINT REFERENCES household(household_id) ON DELETE RESTRICT;
ALTER TABLE board ADD COLUMN retired_at   TIMESTAMPTZ;

CREATE INDEX idx_board_household_id ON board(household_id);
-- Partial index for the default (non-retired) listing population, mirroring
-- the idx_plant_active convention from 001_initial_schema.
CREATE INDEX idx_board_active ON board(board_id) WHERE retired_at IS NULL;

-- ── Backfill: existing boards → Unadopted (FR70.1) ──────────────────────────
-- Every pre-existing board gets the seeded Unadopted household: a
-- board_ownership SCD2 row as the record of truth, mirrored onto
-- board.household_id as the current-value cache the repository helpers and
-- listing queries read.

UPDATE board
SET household_id = (SELECT household_id FROM household WHERE is_unadopted = TRUE);

INSERT INTO board_ownership (board_id, household_id, valid_from)
SELECT board_id, household_id, NOW()
FROM board
WHERE household_id IS NOT NULL;

-- ── New-arrivals guard (A9) ──────────────────────────────────────────────────
-- Unadopted receives no new arrivals after this migration's backfill: any
-- INSERT into board_ownership OR household_membership naming the Unadopted
-- household is refused. household_membership is seeded with zero rows
-- (Unadopted is member-less, FR70.1) and has no backfill of its own, so its
-- trigger guards unconditionally; board_ownership's trigger is created after
-- the backfill INSERT above so that backfill itself is exempt -- everything
-- after this point in the transaction is a genuinely new arrival. The same
-- function covers both tables since it only reads NEW.household_id, which
-- both share.

CREATE FUNCTION enforce_no_unadopted_arrivals() RETURNS TRIGGER AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM household
        WHERE household_id = NEW.household_id AND is_unadopted = TRUE
    ) THEN
        RAISE EXCEPTION 'household % is Unadopted and accepts no new % rows (A9)', NEW.household_id, TG_TABLE_NAME;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_board_ownership_no_unadopted_arrivals
    BEFORE INSERT ON board_ownership
    FOR EACH ROW
    EXECUTE FUNCTION enforce_no_unadopted_arrivals();

CREATE TRIGGER trg_household_membership_no_unadopted_arrivals
    BEFORE INSERT ON household_membership
    FOR EACH ROW
    EXECUTE FUNCTION enforce_no_unadopted_arrivals();

-- ── region.household_id ──────────────────────────────────────────────────────
-- Tree-root only: descendants inherit and must carry NULL. Household is not
-- derivable through the board for regions (a region subtree can span several
-- boards), so this column exists in addition to board.household_id.

ALTER TABLE region ADD COLUMN household_id BIGINT REFERENCES household(household_id) ON DELETE RESTRICT;

CREATE INDEX idx_region_household_id ON region(household_id);

-- Enforce "non-root region's household_id is NULL or equals its root's" with
-- a trigger -- CHECK constraints cannot walk the parent chain.
CREATE FUNCTION enforce_region_household_root() RETURNS TRIGGER AS $$
DECLARE
    root_id     BIGINT;
    root_hh_id  BIGINT;
BEGIN
    IF NEW.parent_region_id IS NULL THEN
        -- This row is itself a root; any household_id (including NULL) is valid.
        RETURN NEW;
    END IF;

    WITH RECURSIVE ancestors AS (
        SELECT region_id, parent_region_id, household_id
        FROM region
        WHERE region_id = NEW.parent_region_id

        UNION ALL

        SELECT r.region_id, r.parent_region_id, r.household_id
        FROM region r
        JOIN ancestors a ON r.region_id = a.parent_region_id
    )
    SELECT region_id, household_id INTO root_id, root_hh_id
    FROM ancestors
    WHERE parent_region_id IS NULL;

    IF NEW.household_id IS NOT NULL AND NEW.household_id IS DISTINCT FROM root_hh_id THEN
        RAISE EXCEPTION 'region % household_id (%) must be NULL or match tree root % household_id (%)',
            NEW.region_id, NEW.household_id, root_id, root_hh_id;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_region_household_root
    BEFORE INSERT OR UPDATE OF household_id, parent_region_id ON region
    FOR EACH ROW
    EXECUTE FUNCTION enforce_region_household_root();

-- ── Backfill: existing region roots → Unadopted (FR70.1) ────────────────────
-- Only tree roots carry household_id; descendants stay NULL and inherit.
-- trg_region_household_root permits this: a root row's own household_id is
-- unconstrained by the trigger.

UPDATE region
SET household_id = (SELECT household_id FROM household WHERE is_unadopted = TRUE)
WHERE parent_region_id IS NULL;

-- ── plant.household_id ───────────────────────────────────────────────────────
-- Carried directly on plant per FR1.1 -- not inherited through region.

ALTER TABLE plant ADD COLUMN household_id BIGINT REFERENCES household(household_id) ON DELETE RESTRICT;

CREATE INDEX idx_plant_household_id ON plant(household_id);

-- ── Backfill: existing plants → Unadopted (FR70.1) ───────────────────────────
-- Every plant resolves to exactly one household (FR1.1) -- unlike board,
-- plant has no "unclaimed" exception, so once backfill completes the column
-- is tightened to NOT NULL.

UPDATE plant
SET household_id = (SELECT household_id FROM household WHERE is_unadopted = TRUE);

ALTER TABLE plant ALTER COLUMN household_id SET NOT NULL;
