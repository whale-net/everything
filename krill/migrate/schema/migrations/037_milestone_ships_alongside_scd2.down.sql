DROP INDEX IF EXISTS milestone_ships_alongside_scope_idx;
DROP INDEX IF EXISTS milestone_ships_alongside_milestone_idx;
DROP INDEX IF EXISTS milestone_ships_alongside_current_id_idx;

-- Superseded revisions share an id; keep only the current one so id can be the key again.
DELETE FROM milestone_ships_alongside WHERE valid_to IS NOT NULL;

CREATE INDEX milestone_ships_alongside_milestone_idx ON milestone_ships_alongside(milestone_id);
CREATE INDEX milestone_ships_alongside_scope_idx ON milestone_ships_alongside(scope_id);

ALTER TABLE milestone_ships_alongside DROP CONSTRAINT IF EXISTS milestone_ships_alongside_pkey;
ALTER TABLE milestone_ships_alongside ADD PRIMARY KEY (id);

ALTER TABLE milestone_ships_alongside
    DROP COLUMN IF EXISTS revision_id,
    DROP COLUMN IF EXISTS valid_from,
    DROP COLUMN IF EXISTS valid_to;
