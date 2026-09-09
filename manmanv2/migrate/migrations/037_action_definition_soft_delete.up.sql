-- Migration 037: Action definition soft-delete with execution-history retention
-- (manmanv2 M3, FR9 + FR10 -- root plan #2080, task #2092)
--
-- Two additive changes, per the 020/033 precedent (no removals, renames, or
-- semantic changes to existing columns):
--
-- 1. FR9: deleting an action definition becomes a SOFT delete. A nullable
--    deleted_at marks the definition deleted; the row (and therefore its
--    action_executions history) survives. The application layer switches
--    DELETE -> UPDATE ... SET deleted_at in the same change that lands this
--    column, so soft-deleted rows only start existing once the code knows
--    about them.
--
-- 2. FR10: action_executions.action_id drops ON DELETE CASCADE so execution
--    history is retained even if a definition row were hard-deleted anyway
--    (e.g. by an operator). Deleting a definition that still has executions
--    now errors at the FK instead of silently destroying history; the
--    normal path never hard-deletes.
--
-- Uniqueness / re-create semantics (issue #2092 Scaffold item 3): the live
-- UNIQUE (definition_level, entity_id, name) from migration 020 is
-- DELIBERATELY KEPT covering soft-deleted rows too, and re-creation
-- RESTORES the soft-deleted row (clears deleted_at) instead of inserting a
-- duplicate. This keeps `ON CONFLICT (definition_level, entity_id, name)`
-- arbiter inference working unchanged for the seed scripts
-- (seed_actions.sh / seed_*_actions.sh), whose inserts would otherwise fail
-- to match a partial unique index and need per-script predicate changes.
-- The outcome is deterministic and identical for UI and seed writers: any
-- insert/update conflict on (definition_level, entity_id, name) revives the
-- soft-deleted row; a live row still conflicts exactly as before.
--
-- The down migration is lossless on purpose: rather than hard-deleting
-- soft-deleted rows (which under the restored CASCADE FK would also destroy
-- their executions -- exactly what this migration exists to prevent), it
-- un-deletes everything before dropping the column.

-- 1. Soft-delete marker (nullable = live).
ALTER TABLE action_definitions
    ADD COLUMN deleted_at TIMESTAMPTZ;

COMMENT ON COLUMN action_definitions.deleted_at IS 'Soft-delete marker (FR9): NULL = live definition; non-NULL = deleted, excluded from listings/execution, restored on re-create at the same (definition_level, entity_id, name)';

-- 2. Retain execution history: replace the CASCADE FK with a plain FK.
DO $$
DECLARE
    fk_name TEXT;
BEGIN
    SELECT conname INTO fk_name
    FROM pg_constraint
    WHERE conrelid = 'action_executions'::regclass
      AND contype = 'f'
      AND confrelid = 'action_definitions'::regclass
      AND pg_get_constraintdef(oid) LIKE '%action_id%';
    IF fk_name IS NULL THEN
        RAISE EXCEPTION 'expected an action_executions -> action_definitions FK, found none';
    END IF;
    EXECUTE format('ALTER TABLE action_executions DROP CONSTRAINT %I', fk_name);
END $$;

ALTER TABLE action_executions
    ADD CONSTRAINT action_executions_action_id_fkey
    FOREIGN KEY (action_id) REFERENCES action_definitions(action_id);

COMMENT ON CONSTRAINT action_executions_action_id_fkey ON action_executions IS 'FR10: no ON DELETE CASCADE -- deleting (or soft-deleting) a definition must never remove its execution history';
