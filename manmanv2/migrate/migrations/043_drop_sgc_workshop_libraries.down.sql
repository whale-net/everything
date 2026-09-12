-- Migration 043 down: recreate sgc_workshop_libraries exactly as it existed
-- immediately before 043's up ran -- i.e. 024's original definition plus
-- 027's ADD COLUMN preset_id/volume_id/installation_path_override. A down
-- migration must restore the schema as of the migration immediately prior,
-- not the table's very first version: migration 040's up.sql (and any other
-- migration between 027 and 043) reads those columns, so a down-then-up
-- round trip through this migration must leave them in place or every later
-- migration that touches this table breaks on re-apply.
--
-- This is a SCHEMA rollback only, not a data restore: rows dropped by 043's
-- up migration are gone. The GC-level table this task's cutover moved
-- resolution to (gameconfig_workshop_libraries, 042) is left untouched by
-- this down migration, so the recreated table starts empty -- re-running
-- 043's up immediately afterwards simply drops an empty table again.
CREATE TABLE IF NOT EXISTS sgc_workshop_libraries (
    sgc_id                     BIGINT NOT NULL REFERENCES server_game_configs(sgc_id) ON DELETE CASCADE,
    library_id                 BIGINT NOT NULL REFERENCES workshop_libraries(library_id) ON DELETE CASCADE,
    created_at                 TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    preset_id                  BIGINT NULL REFERENCES game_addon_path_presets(preset_id) ON DELETE SET NULL,
    volume_id                  BIGINT NULL REFERENCES game_config_volumes(volume_id) ON DELETE SET NULL,
    installation_path_override TEXT NULL,
    PRIMARY KEY (sgc_id, library_id)
);
CREATE INDEX IF NOT EXISTS idx_sgc_workshop_libraries_sgc_id ON sgc_workshop_libraries(sgc_id);
CREATE INDEX IF NOT EXISTS idx_sgc_workshop_libraries_library_id ON sgc_workshop_libraries(library_id);
