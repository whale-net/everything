-- Migration 012: analytical views for Grafana dashboards
--
-- All views are in the public schema with a v_ prefix.
-- They are plain (non-materialised) views — TimescaleDB's query planner pushes
-- time-range predicates through them and prunes hypertable chunks normally.
--
-- Layer 1: dimension helpers  (v_region_path, v_sensor_current,
--                               v_board_state_history, v_board_state_current)
-- Layer 2: wide enriched fact (v_sensor_reading_enriched)
-- Layer 3: fanout joins       (v_sensor_reading_with_plant,
--                               v_sensor_reading_with_config_debug)

-- ── Layer 1: v_region_path ───────────────────────────────────────────────────
-- Materialises the full root→leaf path for every region.
-- path_ids / path_names are arrays ordered from root to leaf.
-- path_name is the human-readable ' / '-delimited string.

CREATE VIEW v_region_path AS
WITH RECURSIVE path AS (
    -- Anchor: top-level regions (no parent).
    SELECT
        r.region_id,
        r.name,
        r.parent_region_id,
        ARRAY[r.region_id]::BIGINT[] AS path_ids,
        ARRAY[r.name]::TEXT[]        AS path_names,
        r.name::TEXT                 AS path_name,
        0                            AS depth
    FROM region r
    WHERE r.parent_region_id IS NULL

    UNION ALL

    -- Recursive step: attach children.
    SELECT
        r.region_id,
        r.name,
        r.parent_region_id,
        p.path_ids   || r.region_id,
        p.path_names || r.name,
        p.path_name  || ' / ' || r.name,
        p.depth + 1
    FROM region r
    JOIN path p ON p.region_id = r.parent_region_id
)
SELECT
    region_id,
    name,
    parent_region_id,
    path_ids,
    path_names,
    path_name,
    depth
FROM path;

-- ── Layer 1: v_sensor_current ────────────────────────────────────────────────
-- One row per sensor with current-state joins resolved.
-- sensor_name comes from sensor_name_history (SCD2); sensor.name is kept in
-- sync as a cache but sensor_name_history is the authoritative source.

CREATE VIEW v_sensor_current AS
SELECT
    s.sensor_id,
    s.board_id,
    b.device_id,
    snh.name                AS sensor_name,
    s.unit                  AS sensor_unit,
    s.sensor_type_id,
    st.name                 AS sensor_type_name,
    st.default_unit,
    s.sensor_chip_id,
    sc.name                 AS sensor_chip_name,
    s.region_id,
    r.name                  AS region_name,
    rp.path_name            AS region_path_name,
    s.i2c_address,
    s.mux_path,
    s.registered_at
FROM sensor s
JOIN  board b              ON b.board_id        = s.board_id
JOIN  sensor_type st       ON st.sensor_type_id = s.sensor_type_id
LEFT JOIN sensor_name_history snh
                           ON snh.sensor_id     = s.sensor_id
                          AND snh.valid_to IS NULL
LEFT JOIN sensor_chip sc   ON sc.sensor_chip_id = s.sensor_chip_id
LEFT JOIN region r         ON r.region_id       = s.region_id
LEFT JOIN v_region_path rp ON rp.region_id      = s.region_id;

-- ── Layer 1: v_board_state_history ───────────────────────────────────────────
-- Flattens device_config into SCD2-shaped rows using a window function.
-- Each accepted config becomes one row; valid_to is the acked_at of the next
-- accepted version (NULL means still active).
-- This is the equivalent of a "board version history" table.

CREATE VIEW v_board_state_history AS
SELECT
    dc.board_id,
    b.device_id,
    dc.config_id,
    dc.version,
    dc.config_json,
    dc.pushed_at,
    dc.acked_at                                                           AS valid_from,
    LEAD(dc.acked_at) OVER (PARTITION BY dc.board_id ORDER BY dc.version) AS valid_to
FROM device_config dc
JOIN board b ON b.board_id = dc.board_id
WHERE dc.accepted = TRUE;

-- ── Layer 1: v_board_state_current ───────────────────────────────────────────
-- One row per board: the currently active (latest accepted) device config.

CREATE VIEW v_board_state_current AS
SELECT * FROM v_board_state_history
WHERE valid_to IS NULL;

-- ── Layer 2: v_sensor_reading_enriched ───────────────────────────────────────
-- One row per sensor_reading. The workhorse view for most Grafana panels.
--
-- Key temporal choices:
--   region_id / region_name / region_path_* — from sensor_reading.region_id
--     (the snapshot taken at insert time), NOT the sensor's current region.
--     This preserves the correct location even when sensors are moved.
--   config_version — from sensor_reading.config_version (stamped at insert).
--   sensor_name — from v_sensor_current (current name; not point-in-time).
--     For dashboards showing live data this is almost always what you want.
--
-- config_json is intentionally omitted (heavy JSONB); see
-- v_sensor_reading_with_config_debug for the debug variant.

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
-- Extends v_sensor_reading_enriched with plants that were active in the reading's
-- region at recorded_at. One row per (reading × active plant).
--
-- Readings in regions with no active plant still appear (LEFT JOIN) with NULL
-- plant fields — no readings are dropped.
--
-- A region may host multiple plants simultaneously, so this view can return
-- more rows than v_sensor_reading_enriched. Use it for plant / plant_type
-- slices; use v_sensor_reading_enriched for raw metric panels.

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

-- ── Layer 3: v_sensor_reading_with_config_debug ───────────────────────────────
-- Extends v_sensor_reading_enriched with device_config.config_json for the
-- config version that was active when the reading was taken.
-- Useful for "what exact sensor setup was running when this anomaly appeared?"
-- Kept separate to avoid the cost of loading JSONB on every panel query.

CREATE VIEW v_sensor_reading_with_config_debug AS
SELECT
    e.*,
    dc.config_json         AS device_config_json
FROM v_sensor_reading_enriched e
LEFT JOIN device_config dc ON dc.board_id = e.board_id
                          AND dc.version   = e.config_version;
