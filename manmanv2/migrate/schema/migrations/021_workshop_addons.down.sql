-- Rollback workshop_addons table

DROP TRIGGER IF EXISTS update_workshop_addons_updated_at ON workshop_addons;
DROP INDEX IF EXISTS idx_workshop_addons_deprecated;
DROP INDEX IF EXISTS idx_workshop_addons_platform;
DROP INDEX IF EXISTS idx_workshop_addons_workshop_id;
DROP INDEX IF EXISTS idx_workshop_addons_game_id;
DROP TABLE IF EXISTS workshop_addons;
