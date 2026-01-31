-- Drop backup tracking from sessions
ALTER TABLE sessions DROP COLUMN IF EXISTS restored_from_backup_id;

-- Drop backups table
DROP TABLE IF EXISTS backups;
