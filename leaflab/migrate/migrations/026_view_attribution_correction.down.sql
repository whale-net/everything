-- Migration 021 down: restore v_sensor_reading_with_plant to its
-- pre-migration-021 (migration 012) definition.
--
-- CREATE OR REPLACE VIEW cannot drop a trailing column, so reversing the
-- household_id addition requires DROP + CREATE rather than another
-- CREATE OR REPLACE.

DROP VIEW v_sensor_reading_with_plant;

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
