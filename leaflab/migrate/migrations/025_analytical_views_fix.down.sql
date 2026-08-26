-- Reverse migration 025: restore previous analytical views definitions.
--
-- This migration reverts the corrected v_sensor_reading_with_plant view
-- back to its original (defective) definition that used exact region equality.

DROP VIEW IF EXISTS v_sensor_reading_with_plant;

-- Restore original v_sensor_reading_with_plant view (defective: re-attributes on plant moves)
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
