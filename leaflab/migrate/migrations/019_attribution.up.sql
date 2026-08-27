-- Migration 019: nearest-ancestor plant attribution SQL function (FR23)
--
-- Scaffold only (this task's Scaffold phase): attribute_region_plants
-- raises NOT IMPLEMENTED until this task's Implementation phase fills in
-- the walk-up-the-tree logic. It is the SQL twin of
-- leaflab/api/attribution.Resolver.ResolvePlants -- NFR1.c requires the two
-- to agree by construction, which is why both are scaffolded in this same
-- task rather than one now and one in a later task.
--
-- Shape, once implemented: walk region.parent_region_id from p_region_id
-- toward the root (reusing v_region_path from migration 012 rather than a
-- second recursive CTE); the first region -- including p_region_id itself
-- -- with at least one plant whose plant_region_history interval contains
-- p_at is the attributing region (A11: nearest ancestor only, never every
-- ancestor level). Every active plant in that one region is returned, so a
-- caller can both disclose siblings (FR23) and aggregate per attributed
-- region before projecting onto plants (FR20.1's no-double-counting rule).
--
-- Intended callers, once implemented: FR72's repointed
-- v_sensor_reading_with_plant (a view can CROSS JOIN LATERAL a
-- set-returning function) and the read path's per-region aggregate (FR20).
-- Neither is wired to this function yet -- that repoint is out of this
-- task's scope per the issue's "moved out of the plant phase" note.
CREATE FUNCTION attribute_region_plants(p_region_id BIGINT, p_at TIMESTAMPTZ)
RETURNS TABLE (attributed_region_id BIGINT, plant_id BIGINT, plant_name TEXT)
AS $$
BEGIN
    RAISE EXCEPTION 'attribute_region_plants: not implemented (Implementation phase, FR23)';
END;
$$ LANGUAGE plpgsql;
