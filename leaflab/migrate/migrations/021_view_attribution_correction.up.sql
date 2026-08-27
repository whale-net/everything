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
-- Scaffold only (this task's Scaffold phase): this migration file claims
-- the number and adds household_id (a pure addition -- no
-- attribution-correctness implication), but the plant join below is
-- UNCHANGED from migration 012 -- it still joins p.region_id = e.region_id
-- (current placement, exact equality), which is the exact defect FR72
-- corrects (moving a plant retroactively rewrites every reading it ever
-- produced).
--
-- This task's Implementation phase repoints the join below to
-- attribute_region_plants (migration 019's SQL function -- the SQL twin of
-- leaflab/api/attribution.Resolver.ResolvePlants, NFR1.c), via
-- `LEFT JOIN LATERAL attribute_region_plants(e.region_id, e.recorded_at)`,
-- reusing v_region_path for the ancestor walk rather than a second
-- recursive CTE or a copy of the walk-up-the-tree logic. See migration
-- 019's doc comment and issue #1361 for the full shape, including:
--   - the API read path deliberately does NOT query this view (FR71 serves
--     series/summaries from the tiers instead) -- NFR3.3/NFR5 do not cover
--     it, and its cost profile changes materially once attribution is
--     per-row nearest-ancestor resolution instead of a flat equality join;
--   - the audit of every other migration-012 view for the same defect
--     (v_sensor_reading_enriched, v_sensor_current, v_board_state_*,
--     v_sensor_reading_with_config_debug) and the Grafana panel-level
--     announcement, both to be written up in the Implementation phase
--     commit and the PR body.
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
LEFT JOIN plant p
       ON p.region_id  = e.region_id
      AND p.created_at <= e.recorded_at
      AND (p.removed_at IS NULL OR p.removed_at > e.recorded_at)
LEFT JOIN plant_type pt ON pt.plant_type_id = p.plant_type_id
LEFT JOIN region root_r ON root_r.region_id = e.region_path_ids[1];
