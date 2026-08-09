-- Rollback App Registry Version Allocation schema (AR-5a)

DROP TABLE IF EXISTS version_allocation;

DROP INDEX IF EXISTS artifact_version_order_idx;

ALTER TABLE artifact DROP COLUMN IF EXISTS version_major;
ALTER TABLE artifact DROP COLUMN IF EXISTS version_minor;
ALTER TABLE artifact DROP COLUMN IF EXISTS version_patch;
