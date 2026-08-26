-- Migration 025: fix analytical views attribution defect.
--
-- Defect: v_sensor_reading_with_plant previously joined plants using exact
-- region equality (p.region_id = e.region_id), causing all historical readings
-- to be re-attributed when a plant moved regions.
--
-- Fix: Join through plant_region_history using value-at-time-T semantics
-- (valid_from <= recorded_at AND (valid_to IS NULL OR valid_to > recorded_at))
-- to preserve correct plant attribution over time. This implements FR72.
--
-- Also adds household_id column per specification.

DROP VIEW IF EXISTS v_sensor_reading_with_plant;

-- Corrected v_sensor_reading_with_plant view
-- Implements FR72: fixes attribution defect by joining through plant_region_history
-- with value-at-time-T semantics at recorded_at. Uses nearest-ancestor resolution
-- per FR23 to find the nearest ancestor region holding an active plant at each
-- reading's timestamp, then returns all active plants in that attributed region
-- (sibling disclosure per FR23). Readings in regions with no plant in any ancestor
-- still appear with NULL plant fields (LEFT JOIN, no readings dropped).
CREATE VIEW v_sensor_reading_with_plant AS
WITH reading_attribution AS (
    -- For each reading, determine its attributed region using nearest-ancestor logic.
    -- The attributed region is the first ancestor (including the reading's own region)
    -- that has at least one active plant at the reading's recorded_at timestamp.
    -- Returns NULL if no ancestor has an active plant.
    SELECT
        e.reading_id,
        e.recorded_at,
        e.region_id,
        (
            WITH RECURSIVE region_ancestors AS (
                -- Start with the reading's region
                SELECT region_id, parent_region_id, 0 AS ancestor_depth
                FROM region
                WHERE region_id = e.region_id

                UNION ALL

                -- Walk upward to ancestors
                SELECT r.region_id, r.parent_region_id, ra.ancestor_depth + 1
                FROM region r
                JOIN region_ancestors ra ON r.region_id = ra.parent_region_id
            )
            SELECT ra.region_id
            FROM region_ancestors ra
            WHERE EXISTS (
                SELECT 1 FROM plant_region_history prh
                WHERE prh.region_id = ra.region_id
                  AND prh.valid_from <= e.recorded_at
                  AND (prh.valid_to IS NULL OR prh.valid_to > e.recorded_at)
            )
            ORDER BY ra.ancestor_depth ASC
            LIMIT 1
        ) AS attributed_region_id
    FROM v_sensor_reading_enriched e
)
SELECT
    e.*,
    p.household_id,
    p.plant_id,
    p.name                 AS plant_name,
    pt.plant_type_id,
    pt.common_name         AS plant_common_name,
    pt.species             AS plant_species
FROM v_sensor_reading_enriched e
LEFT JOIN reading_attribution ra ON ra.reading_id = e.reading_id
LEFT JOIN plant p
    ON p.plant_id IN (
        SELECT prh.plant_id
        FROM plant_region_history prh
        WHERE prh.region_id = ra.attributed_region_id
          AND prh.valid_from <= ra.recorded_at
          AND (prh.valid_to IS NULL OR prh.valid_to > ra.recorded_at)
    )
LEFT JOIN plant_type pt ON pt.plant_type_id = p.plant_type_id;
