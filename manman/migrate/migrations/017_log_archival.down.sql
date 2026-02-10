-- Rollback log archival changes

-- Drop indexes
DROP INDEX IF EXISTS idx_log_refs_state;
DROP INDEX IF EXISTS idx_log_refs_sgc_minute;

-- Drop foreign key constraint
ALTER TABLE log_references DROP CONSTRAINT IF EXISTS fk_log_references_sgc;

-- Drop columns
ALTER TABLE log_references DROP COLUMN IF EXISTS appended_at;
ALTER TABLE log_references DROP COLUMN IF EXISTS state;
ALTER TABLE log_references DROP COLUMN IF EXISTS minute_timestamp;
ALTER TABLE log_references DROP COLUMN IF EXISTS sgc_id;
