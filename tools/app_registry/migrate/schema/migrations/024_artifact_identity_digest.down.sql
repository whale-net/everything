-- Rollback: App Registry — Content identity digest

DROP INDEX IF EXISTS artifact_identity_lookup_idx;
DROP INDEX IF EXISTS artifact_identity_digest_idx;
ALTER TABLE artifact DROP COLUMN IF EXISTS identity_digest;
