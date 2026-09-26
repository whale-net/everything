-- Rollback workshop_installations table

DROP TRIGGER IF EXISTS update_workshop_installations_updated_at ON workshop_installations;
DROP INDEX IF EXISTS idx_workshop_installations_status;
DROP INDEX IF EXISTS idx_workshop_installations_addon_id;
DROP INDEX IF EXISTS idx_workshop_installations_sgc_id;
DROP TABLE IF EXISTS workshop_installations;
