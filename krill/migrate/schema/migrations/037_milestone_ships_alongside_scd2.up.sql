-- 037_milestone_ships_alongside_scd2: gives milestone_ships_alongside the
-- repo-wide SCD2 pair so the shared nextSiblingPosition helper (which
-- filters valid_to IS NULL) and current-revision reads work on it.
-- Existing rows land current: valid_from is backfilled from created_at.
ALTER TABLE milestone_ships_alongside
    ADD COLUMN IF NOT EXISTS revision_id UUID NOT NULL DEFAULT gen_random_uuid(),
    ADD COLUMN IF NOT EXISTS valid_from  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ADD COLUMN IF NOT EXISTS valid_to    TIMESTAMPTZ NULL;

UPDATE milestone_ships_alongside SET valid_from = created_at;

ALTER TABLE milestone_ships_alongside DROP CONSTRAINT IF EXISTS milestone_ships_alongside_pkey;
ALTER TABLE milestone_ships_alongside ADD PRIMARY KEY (revision_id);

CREATE UNIQUE INDEX IF NOT EXISTS milestone_ships_alongside_current_id_idx
    ON milestone_ships_alongside(id) WHERE valid_to IS NULL;

DROP INDEX IF EXISTS milestone_ships_alongside_milestone_idx;
CREATE INDEX milestone_ships_alongside_milestone_idx ON milestone_ships_alongside(milestone_id) WHERE valid_to IS NULL;

DROP INDEX IF EXISTS milestone_ships_alongside_scope_idx;
CREATE INDEX milestone_ships_alongside_scope_idx ON milestone_ships_alongside(scope_id) WHERE valid_to IS NULL;
