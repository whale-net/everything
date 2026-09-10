-- gameconfig_workshop_libraries: the GameConfig-level Workshop library
-- attachment table (M6 #2361, plan #2359). Deploy-time resolution still
-- reads sgc_workshop_libraries (024) -- this table is purely additive in
-- this migration and is not yet read by any resolution path. It mirrors
-- sgc_workshop_libraries' override columns (preset_id/volume_id/
-- installation_path_override, added in 027) exactly so nothing configured
-- today at SGC scope is lossy once backfilled to GC scope.
--
-- workshop_library_migration_conflicts / _candidates: durable record of an
-- FR12 conflict -- two SGCs of the same GameConfig disagreeing on their
-- attached library set (or on override columns for the same library_id).
-- These exist so the backfill never has to guess: a conflicting GameConfig
-- gets a conflict row instead of a guessed gameconfig_workshop_libraries
-- row, and the conflict survives a restart until a Server Manager resolves
-- it (dependent resolution task).
--
-- NFR1: this is step one of fully retiring SGC-scoped attachment, not a
-- permanent parallel structure -- sgc_workshop_libraries is dropped only in
-- the dependent retirement task, once the API/UI have cut over to this
-- table.

CREATE TABLE IF NOT EXISTS gameconfig_workshop_libraries (
    config_id                  BIGINT NOT NULL REFERENCES game_configs(config_id) ON DELETE CASCADE,
    library_id                 BIGINT NOT NULL REFERENCES workshop_libraries(library_id) ON DELETE CASCADE,
    preset_id                  BIGINT NULL REFERENCES game_addon_path_presets(preset_id) ON DELETE SET NULL,
    volume_id                  BIGINT NULL REFERENCES game_config_volumes(volume_id) ON DELETE SET NULL,
    installation_path_override TEXT   NULL,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (config_id, library_id)
);
CREATE INDEX IF NOT EXISTS idx_gameconfig_workshop_libraries_config_id
    ON gameconfig_workshop_libraries(config_id);
CREATE INDEX IF NOT EXISTS idx_gameconfig_workshop_libraries_library_id
    ON gameconfig_workshop_libraries(library_id);

CREATE TABLE IF NOT EXISTS workshop_library_migration_conflicts (
    conflict_id         BIGSERIAL PRIMARY KEY,
    config_id           BIGINT NOT NULL REFERENCES game_configs(config_id) ON DELETE CASCADE,
    detected_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at          TIMESTAMPTZ NULL,
    resolution           TEXT NULL CHECK (resolution IN ('union', 'override')),
    resolved_library_id  BIGINT NULL REFERENCES workshop_libraries(library_id) ON DELETE SET NULL
);
-- "Unresolved conflicts for this GameConfig" -- the query the resolution UI
-- needs (dependent task).
CREATE INDEX IF NOT EXISTS idx_workshop_library_migration_conflicts_unresolved
    ON workshop_library_migration_conflicts(config_id)
    WHERE resolved_at IS NULL;

CREATE TABLE IF NOT EXISTS workshop_library_migration_conflict_candidates (
    conflict_id BIGINT NOT NULL REFERENCES workshop_library_migration_conflicts(conflict_id) ON DELETE CASCADE,
    library_id  BIGINT NOT NULL REFERENCES workshop_libraries(library_id) ON DELETE CASCADE,
    sgc_id      BIGINT NOT NULL,
    PRIMARY KEY (conflict_id, library_id, sgc_id)
);
