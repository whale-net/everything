-- Migration 043 down: recreate sgc_workshop_libraries exactly as migration
-- 024's up.sql defined it (before 027 added its now-irrelevant preset_id/
-- volume_id/installation_path_override override columns).
--
-- This is a SCHEMA rollback only, not a data restore: rows dropped by 043's
-- up migration are gone. The GC-level table this task's cutover moved
-- resolution to (gameconfig_workshop_libraries, 042) is left untouched by
-- this down migration, so the recreated table starts empty -- re-running
-- 043's up immediately afterwards simply drops an empty table again.
CREATE TABLE IF NOT EXISTS sgc_workshop_libraries (
    sgc_id     BIGINT NOT NULL REFERENCES server_game_configs(sgc_id) ON DELETE CASCADE,
    library_id BIGINT NOT NULL REFERENCES workshop_libraries(library_id) ON DELETE CASCADE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (sgc_id, library_id)
);
CREATE INDEX IF NOT EXISTS idx_sgc_workshop_libraries_sgc_id ON sgc_workshop_libraries(sgc_id);
CREATE INDEX IF NOT EXISTS idx_sgc_workshop_libraries_library_id ON sgc_workshop_libraries(library_id);
