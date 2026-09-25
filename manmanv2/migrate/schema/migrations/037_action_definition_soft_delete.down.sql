-- Migration 037 down: revert action-definition soft-delete scaffolding.
--
-- Lossless by design (see up migration): un-delete soft-deleted rows rather
-- than hard-deleting them -- hard-deleting under the CASCADE FK restored
-- below would also destroy their action_executions history, which this
-- migration exists to protect. Dropping the column removes the only record
-- of which rows were soft-deleted, which is the accepted loss in down.

-- 1. Un-delete everything (the column is about to disappear).
UPDATE action_definitions SET deleted_at = NULL;

-- 2. Drop the soft-delete marker.
ALTER TABLE action_definitions
    DROP COLUMN deleted_at;

-- 3. Restore the original ON DELETE CASCADE FK on action_executions.
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
    FOREIGN KEY (action_id) REFERENCES action_definitions(action_id) ON DELETE CASCADE;
