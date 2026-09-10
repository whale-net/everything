-- Migration 018: historical region roll-up in the reading-enrichment views (FR4)
--
-- A reading must roll up under the region path as it existed at the reading's
-- recorded_at, not the region's current parents: re-parenting a region (FR3)
-- must not retroactively change which region a pre-existing reading rolls up
-- under.
--
-- Redefines v_sensor_reading_enriched so its region-path resolution walks
-- region_parent_history filtered to
--   valid_from <= recorded_at AND (valid_to IS NULL OR valid_to > recorded_at)
-- at each ancestor step (recursive CTE in a correlated LATERAL), instead of
-- joining v_region_path's current-parent-only computation.
--
-- v_region_path itself is UNCHANGED — it keeps resolving the current tree for
-- FR6's tree/drill-down view (a current-state view).
--
-- Everything built on the enriched view (v_sensor_reading_with_plant,
-- v_sensor_reading_with_config_debug) inherits the historical roll-up by
-- layering. Postgres cannot alter a view underneath a redefinition of its
-- dependency, so both dependents are dropped and recreated verbatim from
-- migration 012 — their definitions are unchanged.
--
-- The walk (per reading, in a correlated LATERAL so outer time-range /
-- sensor filters still prune hypertable chunks and index-scan the readings):
--   Anchor: the reading's own region — the snapshot stamped at insert time;
--   its identity needs no history lookup. A reading with a NULL region
--   (unplaced sensor) matches no anchor row, so the LEFT JOIN keeps it with
--   NULL path fields, exactly as the previous v_region_path join did.
--   Step: resolve the current node's ancestor from its region_parent_history
--   row that was open at the reading's recorded_at, prepending the ancestor
--   so path_ids / path_names / path_name stay ordered root → leaf like
--   v_region_path's convention.
--   Termination: on a row whose parent_region_id IS NULL — the recorded
--   top-of-tree — never on a missing row. Top-level-ness is always recorded
--   (exactly one open history row per region, NULL parent for top-level;
--   migration 017, #2311). The terminal row (greatest depth) carries the full
--   path; LIMIT 1 picks it.
--   A reading whose region has no history row covering recorded_at (e.g.
--   readings recorded before migration 017 backfilled the table) resolves to
--   its snapshot region alone, with no ancestors.

DROP VIEW IF EXISTS v_sensor_reading_with_config_debug;
DROP VIEW IF EXISTS v_sensor_reading_with_plant;
DROP VIEW IF EXISTS v_sensor_reading_enriched;

CREATE VIEW v_sensor_reading_enriched AS
SELECT
    sr.reading_id,
    sr.recorded_at,
    sr.value,
    sr.valid,
    sr.uptime_s,
    -- Sensor identity
    sr.sensor_id,
    sc.sensor_name,
    sc.sensor_unit,
    sc.sensor_type_id,
    sc.sensor_type_name,
    sc.sensor_chip_id,
    sc.sensor_chip_name,
    -- Board identity
    sc.board_id,
    sc.device_id,
    -- Region (snapshot at insert — historically accurate), ancestor chain
    -- resolved as of recorded_at via the LATERAL walk below
    sr.region_id,
    r.name                 AS region_name,
    hp.path_ids            AS region_path_ids,
    hp.path_names          AS region_path_names,
    hp.path_name           AS region_path_name,
    -- Config version (stamped at insert)
    sr.config_version,
    dc.pushed_at           AS device_config_pushed_at,
    dc.accepted            AS device_config_accepted
FROM sensor_reading sr
LEFT JOIN v_sensor_current sc  ON sc.sensor_id  = sr.sensor_id
LEFT JOIN region r             ON r.region_id   = sr.region_id
LEFT JOIN LATERAL (
    WITH RECURSIVE walk AS (
        -- Anchor: the reading's own region (insert-time snapshot).
        SELECT
            sr.region_id AS region_id,
            r0.name,
            ARRAY[sr.region_id]::BIGINT[] AS path_ids,
            ARRAY[r0.name]::TEXT[]        AS path_names,
            r0.name::TEXT                 AS path_name,
            0                             AS depth
        FROM region r0
        WHERE r0.region_id = sr.region_id

        UNION ALL

        -- Step: the ancestor that was the current node's parent at
        -- recorded_at, prepended so the terminal row reads root → leaf.
        SELECT
            rph.parent_region_id,
            pr.name,
            rph.parent_region_id || w.path_ids,
            pr.name || w.path_names,
            pr.name || ' / ' || w.path_name,
            w.depth + 1
        FROM walk w
        JOIN region_parent_history rph
          ON rph.region_id  = w.region_id
         AND rph.valid_from <= sr.recorded_at
         AND (rph.valid_to IS NULL OR rph.valid_to > sr.recorded_at)
        JOIN region pr ON pr.region_id = rph.parent_region_id
        WHERE rph.parent_region_id IS NOT NULL
    )
    -- The walk emits the anchor plus one row per resolved ancestor; the
    -- deepest row is the complete path.
    SELECT walk.path_ids, walk.path_names, walk.path_name
    FROM walk
    ORDER BY walk.depth DESC
    LIMIT 1
) hp ON TRUE
LEFT JOIN device_config dc     ON dc.board_id   = sc.board_id
                              AND dc.version     = sr.config_version;

-- ── Layer 3: v_sensor_reading_with_plant ─────────────────────────────────────
-- Recreated verbatim from migration 012; its region resolution is inherited
-- from v_sensor_reading_enriched, so no change to its own definition.

CREATE VIEW v_sensor_reading_with_plant AS
SELECT
    e.*,
    p.plant_id,
    p.name                 AS plant_name,
    pt.plant_type_id,
    pt.common_name         AS plant_common_name,
    pt.species             AS plant_species
FROM v_sensor_reading_enriched e
LEFT JOIN plant p
       ON p.region_id  = e.region_id
      AND p.created_at <= e.recorded_at
      AND (p.removed_at IS NULL OR p.removed_at > e.recorded_at)
LEFT JOIN plant_type pt ON pt.plant_type_id = p.plant_type_id;

-- ── Layer 3: v_sensor_reading_with_config_debug ──────────────────────────────
-- Recreated verbatim from migration 012.

CREATE VIEW v_sensor_reading_with_config_debug AS
SELECT
    e.*,
    dc.config_json         AS device_config_json
FROM v_sensor_reading_enriched e
LEFT JOIN device_config dc ON dc.board_id = e.board_id
                          AND dc.version   = e.config_version;