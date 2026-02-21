-- Rollback workshop_libraries tables

-- Drop indexes and triggers first
DROP INDEX IF EXISTS idx_workshop_library_refs_child;
DROP INDEX IF EXISTS idx_workshop_library_refs_parent;
DROP INDEX IF EXISTS idx_workshop_library_addons_addon_id;
DROP INDEX IF EXISTS idx_workshop_library_addons_library_id;
DROP TRIGGER IF EXISTS update_workshop_libraries_updated_at ON workshop_libraries;
DROP INDEX IF EXISTS idx_workshop_libraries_game_id;

-- Drop tables in reverse order (respecting foreign key dependencies)
DROP TABLE IF EXISTS workshop_library_references;
DROP TABLE IF EXISTS workshop_library_addons;
DROP TABLE IF EXISTS workshop_libraries;
