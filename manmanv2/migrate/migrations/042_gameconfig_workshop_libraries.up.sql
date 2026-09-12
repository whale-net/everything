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

-- preset_id/volume_id/installation_path_override are denormalized onto this
-- table at detection time (statement 3 below), rather than re-derived from
-- sgc_workshop_libraries at resolution time, because resolution
-- (GameConfigWorkshopLibraryRepository.ResolveConflict, dependent task
-- #2368) can happen after sgc_workshop_libraries is dropped (043, NFR1) --
-- 043's own guard explicitly permits an unresolved conflict to survive the
-- drop as long as its GameConfig already has some gameconfig_workshop_libraries
-- rows (see 043's comment). Deriving these at resolution time would mean
-- ResolveConflict joins a table that may no longer exist; capturing them
-- here means resolution never needs sgc_workshop_libraries at all.
CREATE TABLE IF NOT EXISTS workshop_library_migration_conflict_candidates (
    conflict_id                BIGINT NOT NULL REFERENCES workshop_library_migration_conflicts(conflict_id) ON DELETE CASCADE,
    library_id                 BIGINT NOT NULL REFERENCES workshop_libraries(library_id) ON DELETE CASCADE,
    sgc_id                     BIGINT NOT NULL,
    preset_id                  BIGINT NULL REFERENCES game_addon_path_presets(preset_id) ON DELETE SET NULL,
    volume_id                  BIGINT NULL REFERENCES game_config_volumes(volume_id) ON DELETE SET NULL,
    installation_path_override TEXT   NULL,
    PRIMARY KEY (conflict_id, library_id, sgc_id)
);

-- FR11 backfill + FR12 conflict capture. Split into three idempotent
-- statements rather than one chained CTE so each guard is independently
-- obvious and independently safe to re-run (re-running this migration, or
-- running it against a partially-migrated DB, must not duplicate rows or
-- re-open a resolved conflict):
--   1. gameconfig_workshop_libraries writes are guarded by
--      ON CONFLICT (config_id, library_id) DO NOTHING.
--   2. workshop_library_migration_conflicts writes are guarded by
--      NOT EXISTS for that config_id -- a config that already has a
--      conflict record (resolved or not) is left alone.
--   3. workshop_library_migration_conflict_candidates writes are guarded by
--      NOT EXISTS for that conflict_id -- candidates are only (re)written
--      for a conflict that doesn't have any yet.
--
-- "SGC signature" (the shared building block, recomputed per statement
-- since it's a plain, side-effect-free CTE): for every SGC that has at
-- least one sgc_workshop_libraries row, its full attached-library set,
-- overrides included, canonicalized to one comparable text array. SGCs
-- with zero attachments never produce a signature -- deliberately, so a
-- lone attaching SGC among otherwise-bare siblings is not treated as
-- "disagreeing" with an empty set. Two SGCs of the same GameConfig agree
-- iff their signatures are equal; differing override columns on the same
-- library_id changes the signature just like a differing library_id set
-- does, so both count as a conflict per the issue's FR12 description.

-- Statement 1: non-conflicting configs -- insert the one agreed-upon
-- attachment set (any attaching SGC's rows; they're all identical by
-- construction) at GC level.
WITH sgc_signature AS (
    SELECT
        sgc.game_config_id AS config_id,
        sgc.sgc_id AS sgc_id,
        array_agg(
            swl.library_id::text || ':' ||
            COALESCE(swl.preset_id::text, '') || ':' ||
            COALESCE(swl.volume_id::text, '') || ':' ||
            COALESCE(swl.installation_path_override, '')
            ORDER BY swl.library_id
        ) AS signature
    FROM server_game_configs sgc
    JOIN sgc_workshop_libraries swl ON swl.sgc_id = sgc.sgc_id
    GROUP BY sgc.game_config_id, sgc.sgc_id
),
config_signatures AS (
    SELECT config_id, COUNT(DISTINCT signature) AS distinct_signature_count
    FROM sgc_signature
    GROUP BY config_id
),
representative_sgc AS (
    SELECT s.config_id, MIN(s.sgc_id) AS sgc_id
    FROM sgc_signature s
    JOIN config_signatures cs ON cs.config_id = s.config_id AND cs.distinct_signature_count = 1
    GROUP BY s.config_id
)
INSERT INTO gameconfig_workshop_libraries (config_id, library_id, preset_id, volume_id, installation_path_override)
SELECT rep.config_id, swl.library_id, swl.preset_id, swl.volume_id, swl.installation_path_override
FROM representative_sgc rep
JOIN sgc_workshop_libraries swl ON swl.sgc_id = rep.sgc_id
ON CONFLICT (config_id, library_id) DO NOTHING;

-- Statement 2: conflicting configs (>1 distinct signature among attaching
-- SGCs) that don't already have a durable conflict record -- open one.
WITH sgc_signature AS (
    SELECT
        sgc.game_config_id AS config_id,
        sgc.sgc_id AS sgc_id,
        array_agg(
            swl.library_id::text || ':' ||
            COALESCE(swl.preset_id::text, '') || ':' ||
            COALESCE(swl.volume_id::text, '') || ':' ||
            COALESCE(swl.installation_path_override, '')
            ORDER BY swl.library_id
        ) AS signature
    FROM server_game_configs sgc
    JOIN sgc_workshop_libraries swl ON swl.sgc_id = sgc.sgc_id
    GROUP BY sgc.game_config_id, sgc.sgc_id
),
config_signatures AS (
    SELECT config_id, COUNT(DISTINCT signature) AS distinct_signature_count
    FROM sgc_signature
    GROUP BY config_id
)
INSERT INTO workshop_library_migration_conflicts (config_id)
SELECT cs.config_id
FROM config_signatures cs
WHERE cs.distinct_signature_count > 1
  AND NOT EXISTS (
      SELECT 1 FROM workshop_library_migration_conflicts c WHERE c.config_id = cs.config_id
  );

-- Statement 3: for every conflict row that doesn't have candidates yet
-- (freshly opened by statement 2, or left incomplete by an interrupted
-- prior run), record one candidate row per (library_id, sgc_id) pair
-- across all of that config's attaching SGCs -- this is how "each distinct
-- override variant" ends up recorded: two SGCs attaching the same
-- library_id with different overrides are, by construction, two different
-- sgc_id rows, so both appear as separate candidates.
INSERT INTO workshop_library_migration_conflict_candidates (conflict_id, library_id, sgc_id, preset_id, volume_id, installation_path_override)
SELECT c.conflict_id, swl.library_id, swl.sgc_id, swl.preset_id, swl.volume_id, swl.installation_path_override
FROM workshop_library_migration_conflicts c
JOIN server_game_configs sgc ON sgc.game_config_id = c.config_id
JOIN sgc_workshop_libraries swl ON swl.sgc_id = sgc.sgc_id
WHERE NOT EXISTS (
    SELECT 1 FROM workshop_library_migration_conflict_candidates cc
    WHERE cc.conflict_id = c.conflict_id
);
