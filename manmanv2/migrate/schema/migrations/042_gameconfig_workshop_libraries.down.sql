-- Drop the GC-level Workshop library attachment table and the migration
-- conflict tables. sgc_workshop_libraries (024) is untouched.

DROP TABLE IF EXISTS workshop_library_migration_conflict_candidates;
DROP TABLE IF EXISTS workshop_library_migration_conflicts;
DROP INDEX IF EXISTS idx_gameconfig_workshop_libraries_library_id;
DROP INDEX IF EXISTS idx_gameconfig_workshop_libraries_config_id;
DROP TABLE IF EXISTS gameconfig_workshop_libraries;
