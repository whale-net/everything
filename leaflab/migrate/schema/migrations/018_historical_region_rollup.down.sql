-- Reverse migration 018: restore v_sensor_reading_enriched's current-tree
-- region-path resolution (join v_region_path, as defined in migration 012)
-- and recreate its dependents, in reverse dependency order.

DROP VIEW IF EXISTS v_sensor_reading_with_config_debug;
DROP VIEW IF EXISTS v_sensor_reading_with_plant;
DROP VIEW IF EXISTS v_sensor_reading_enriched;

-- ── Layer 2: v_sensor_reading_enriched (migration 012 definition) ────────────

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
    -- Region (snapshot at insert — historically accurate)
    sr.region_id,
    r.name                 AS region_name,
    rp.path_ids            AS region_path_ids,
    rp.path_names          AS region_path_names,
    rp.path_name           AS region_path_name,
    -- Config version (stamped at insert)
    sr.config_version,
    dc.pushed_at           AS device_config_pushed_at,
    dc.accepted            AS device_config_accepted
FROM sensor_reading sr
LEFT JOIN v_sensor_current sc  ON sc.sensor_id  = sr.sensor_id
LEFT JOIN region r             ON r.region_id   = sr.region_id
LEFT JOIN v_region_path rp     ON rp.region_id  = sr.region_id
LEFT JOIN device_config dc     ON dc.board_id   = sc.board_id
                              AND dc.version     = sr.config_version;

-- ── Layer 3: v_sensor_reading_with_plant ─────────────────────────────────────
-- Recreated verbatim from migration 012.

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