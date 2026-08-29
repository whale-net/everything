-- Migration 021: repoint v_sensor_reading_with_plant to nearest-ancestor
-- attribution, with household_id (FR72, NFR16 views half)
--
-- Picked 021 as the next free number: this branch's own ancestry (plan
-- 1166-v2) only reaches 019 (attribution) on disk, but 1341's
-- household_never_zero_members claimed 020 on a sibling v2 branch, so this
-- migration steps clear of both. State any renumbering in the PR if a
-- collision surfaces at merge time -- 013, 016->017 and 018->019 have all
-- collided before on this plan.
--
-- Implementation phase (this task): the plant join is repointed from
-- p.region_id = e.region_id (current placement, exact equality -- the
-- exact defect FR72 corrects: moving a plant retroactively rewrote every
-- reading it ever produced) to a LATERAL join against
-- attribute_region_plants (migration 019's SQL function -- the SQL twin of
-- leaflab/api/attribution.Resolver.ResolvePlants, NFR1.c requires the two
-- to agree by construction). attribute_region_plants already reuses
-- v_region_path (migration 012) for the ancestor walk, so this view does
-- not implement a second copy of the walk-up-the-tree logic.
--
-- Nearest-ancestor semantics (A11, mirrored from migration 019): a reading
-- attributes to the nearest ancestor region -- including its own -- with at
-- least one plant_region_history row whose interval contains recorded_at,
-- and to every plant active in that ONE region (never plants further up
-- the tree, never every ancestor level). A region may host multiple plants
-- simultaneously, so this view can still return more rows than
-- v_sensor_reading_enriched -- same fan-out contract as before the
-- repoint, just resolved against the correct region.
--
-- attribute_region_plants returns plant_id (and its own plant_name, cast
-- to TEXT to match the Go twin -- see migration 019 / commit 6ed3960f).
-- This view re-joins `plant` by that plant_id instead of reusing the
-- function's plant_name, so plant_name keeps its original
-- VARCHAR(128)-derived type (NFR16's column-type contract) rather than
-- picking up the function's TEXT cast.
--
-- Cost warning (FR72, A11's cost note): this join is now a per-row LATERAL
-- call into a PL/pgSQL function that walks v_region_path and queries
-- plant_region_history, on a view already one row per reading with four
-- joins -- materially more expensive than the single equality join it
-- replaces. The API read path deliberately does NOT query this view: FR71
-- serves series and summaries from the granularity tiers instead, so
-- NFR3.3 and NFR5 do not cover this view and nothing downstream should
-- assume it is still the cheap path. Grafana is this view's only consumer
-- (out-of-scope item 10 keeps it on direct database reads) -- see the PR
-- body for the panel-level attribution-affecting announcement, and
-- leaflab/DATA.md's Analytical Views section for the operator-facing note.
--
-- Audit of every other migration-012 view for the same defect (attribution
-- by a point-in-time-blind current-value join instead of an SCD2/interval
-- lookup) -- see the PR body for the full writeup:
--   - v_sensor_reading_enriched: clean. Its region_id / region_path_* come
--     from sensor_reading.region_id, a snapshot stamped at insert time, not
--     the sensor's current region (migration 012's own doc comment already
--     calls this out).
--   - v_sensor_current, v_board_state_history, v_board_state_current:
--     clean. All three are explicitly "current state" views by name and
--     contract -- v_board_state_history/_current already derive their
--     SCD2-shaped valid_from/valid_to via a LEAD() window function over
--     device_config, an append-only log, not a current-value cache.
--   - v_sensor_reading_with_config_debug: clean. Joins device_config by
--     (board_id, sensor_reading.config_version), the version stamped on
--     the reading at insert time -- not the board's current config.
--   - v_sensor_reading_with_plant: the one defective view (this migration).
--
-- household_id resolves through the region tree root, not the plant or the
-- reading's own region: region.household_id is populated on tree roots
-- only (migration 015), descendants inherit, so this reads
-- v_sensor_reading_enriched's region_path_ids[1] (root-to-leaf, migration
-- 012's v_region_path) rather than sr.region_id directly.
--
-- household_id is appended as the LAST column (after the pre-existing
-- plant_species) because CREATE OR REPLACE VIEW may only append columns,
-- never reorder or remove them -- see this migration's down file for why
-- reversing it needs DROP + CREATE instead.

CREATE OR REPLACE VIEW v_sensor_reading_with_plant AS
SELECT
    e.*,
    p.plant_id,
    p.name                 AS plant_name,
    pt.plant_type_id,
    pt.common_name         AS plant_common_name,
    pt.species             AS plant_species,
    root_r.household_id    AS household_id
FROM v_sensor_reading_enriched e
LEFT JOIN LATERAL attribute_region_plants(e.region_id, e.recorded_at) arp
       ON TRUE
LEFT JOIN plant p ON p.plant_id = arp.plant_id
LEFT JOIN plant_type pt ON pt.plant_type_id = p.plant_type_id
LEFT JOIN region root_r ON root_r.region_id = e.region_path_ids[1];

COMMENT ON VIEW v_sensor_reading_with_plant IS
    'Plant attribution resolves via attribute_region_plants (nearest-ancestor '
    'against plant_region_history at recorded_at, A11) -- NOT a current-region '
    'equality join. The API read path deliberately does not query this view '
    '(FR71 serves series/summaries from the granularity tiers); Grafana is '
    'its only consumer. Cost profile: per-row LATERAL walk, materially more '
    'expensive than a flat equality join -- see migration 021.';
