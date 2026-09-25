-- Migration 043: drop sgc_workshop_libraries (M6 #2370, plan #2359)
--
-- NFR1: fully retires the old per-deployment (SGC-scoped) Workshop library
-- attachment table now that deploy-time resolution
-- (host/workshop/orchestrator.go, api/workshop/manager.go) and every
-- caller (API #2365, Games panel #2367, conflict resolution #2368) read
-- gameconfig_workshop_libraries (042) instead. This is a schema-only
-- cutover: it drops the SGC-scoped table; it does not migrate any further
-- data (042 already did the FR11 backfill).
--
-- Guard: refuse to drop while some GameConfig has an unresolved
-- workshop_library_migration_conflicts row (resolved_at IS NULL) AND no
-- gameconfig_workshop_libraries row yet -- i.e. a conflict a Server Manager
-- was asked to resolve (#2368) that hasn't actually been resolved into a
-- GC-level attachment. Dropping sgc_workshop_libraries in that state would
-- permanently discard the SGC-scoped configuration the conflict exists to
-- capture, with no path back. A resolved conflict, or a conflict whose
-- GameConfig already has SOME gameconfig_workshop_libraries rows (a
-- partial resolution, or a Server Manager writing the union/override
-- outcome directly), does not block the drop.
DO $$
DECLARE
    unresolved_count INT;
BEGIN
    SELECT COUNT(*) INTO unresolved_count
    FROM workshop_library_migration_conflicts c
    WHERE c.resolved_at IS NULL
      AND NOT EXISTS (
          SELECT 1 FROM gameconfig_workshop_libraries gwl
          WHERE gwl.config_id = c.config_id
      );

    IF unresolved_count > 0 THEN
        RAISE EXCEPTION 'migration 043 refused: % GameConfig(s) have an unresolved workshop_library_migration_conflicts row with no gameconfig_workshop_libraries attachments yet -- resolve them via the conflict-resolution surface (#2368) before dropping sgc_workshop_libraries; dropping now would permanently discard that configuration', unresolved_count;
    END IF;
END $$;

DROP INDEX IF EXISTS idx_sgc_workshop_libraries_library_id;
DROP INDEX IF EXISTS idx_sgc_workshop_libraries_sgc_id;
DROP TABLE IF EXISTS sgc_workshop_libraries;
