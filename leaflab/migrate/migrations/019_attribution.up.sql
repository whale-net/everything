-- Migration 019: nearest-ancestor plant attribution SQL function (FR23)
--
-- This is the SQL twin of leaflab/api/attribution.Resolver.ResolvePlants --
-- NFR1.c requires the two to agree by construction, which is why both were
-- scaffolded together in this task and are implemented together here: keep
-- any change to the rule mirrored in both places.
--
-- Walks v_region_path's path_ids (migration 012, root-to-leaf) for
-- p_region_id in reverse -- i.e. from p_region_id itself outward to the
-- root -- rather than a second recursive CTE. The first region --
-- including p_region_id itself -- with at least one plant_region_history
-- row whose interval contains p_at is the attributing region (A11: nearest
-- ancestor only, never every ancestor level; "at least one plant" is
-- p_at-scoped via plant_region_history, per FR19, never plant.region_id's
-- current-value cache). Every plant attributed to that one region is
-- returned, so a caller can both disclose siblings (FR23) and aggregate per
-- attributed region before projecting onto plants (FR20.1's
-- no-double-counting rule) -- never per plant, which would double-count a
-- region with more than one active plant.
--
-- A p_region_id absent from v_region_path (region does not exist) yields an
-- empty result set, not an error -- mirrors the Go twin's
-- ErrRegionNotFound only at the Go layer; a SQL caller distinguishes "no
-- such region" from "region exists, nothing attributes" the same way it
-- already distinguishes any other empty result, via a separate region
-- existence check if it needs to.
--
-- Intended callers, once wired: FR72's repointed v_sensor_reading_with_plant
-- (a view can CROSS JOIN LATERAL a set-returning function) and the read
-- path's per-region aggregate (FR20). Neither is wired to this function yet
-- -- that repoint is out of this task's scope per the issue's "moved out of
-- the plant phase" note.
CREATE FUNCTION attribute_region_plants(p_region_id BIGINT, p_at TIMESTAMPTZ)
RETURNS TABLE (attributed_region_id BIGINT, plant_id BIGINT, plant_name TEXT)
AS $$
DECLARE
    v_path_ids BIGINT[];
    v_attributed_region_id BIGINT;
    v_candidate BIGINT;
    i INT;
BEGIN
    SELECT path_ids INTO v_path_ids FROM v_region_path WHERE region_id = p_region_id;

    IF v_path_ids IS NULL THEN
        RETURN; -- p_region_id does not exist: no path, empty result set.
    END IF;

    FOR i IN REVERSE array_length(v_path_ids, 1)..1 LOOP
        v_candidate := v_path_ids[i];
        IF EXISTS (
            SELECT 1 FROM plant_region_history prh
            WHERE prh.region_id = v_candidate
              AND prh.valid_from <= p_at
              AND (prh.valid_to IS NULL OR prh.valid_to > p_at)
        ) THEN
            v_attributed_region_id := v_candidate;
            EXIT;
        END IF;
    END LOOP;

    IF v_attributed_region_id IS NULL THEN
        RETURN; -- no region on the path has an active plant at p_at.
    END IF;

    RETURN QUERY
    SELECT v_attributed_region_id, p.plant_id, p.name::TEXT
    FROM plant_region_history prh
    JOIN plant p ON p.plant_id = prh.plant_id
    WHERE prh.region_id = v_attributed_region_id
      AND prh.valid_from <= p_at
      AND (prh.valid_to IS NULL OR prh.valid_to > p_at)
    ORDER BY p.plant_id;
END;
$$ LANGUAGE plpgsql;
