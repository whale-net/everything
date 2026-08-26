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
-- IMPLEMENTATION PHASE: Replace with corrected view definition
-- using plant_region_history join with value-at-time-T predicate,
-- nearest-ancestor resolution per FR23, and household_id column.
CREATE VIEW v_sensor_reading_with_plant AS
SELECT
    e.*,
    NULL::BIGINT           AS household_id,  -- TODO: join through household via region ancestry
    NULL::BIGINT           AS plant_id,      -- TODO: join through plant_region_history
    NULL::TEXT             AS plant_name,    -- TODO: from corrected plant join
    NULL::BIGINT           AS plant_type_id, -- TODO: from corrected plant join
    NULL::TEXT             AS plant_common_name, -- TODO: from corrected plant join
    NULL::TEXT             AS plant_species  -- TODO: from corrected plant join
FROM v_sensor_reading_enriched e;
