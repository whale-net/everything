-- Migration 045: fleet-wide backup list index (M7 FR1/FR4, plan #2777, task #2809)
--
-- idx_backups_backup_config_id and idx_backups_status already exist (migration
-- 029) and idx_backups_created_at DESC already exists (migration 004), so the
-- fleet-wide ORDER BY created_at DESC and the backup_config_id/status filters
-- in ListBackups are already covered. volume_id is the one filterable column
-- with no supporting index.
CREATE INDEX IF NOT EXISTS idx_backups_volume_id ON backups(volume_id);
