-- Reverse migration 012: drop all analytical views in reverse dependency order.

DROP VIEW IF EXISTS v_sensor_reading_with_config_debug;
DROP VIEW IF EXISTS v_sensor_reading_with_plant;
DROP VIEW IF EXISTS v_sensor_reading_enriched;
DROP VIEW IF EXISTS v_board_state_current;
DROP VIEW IF EXISTS v_board_state_history;
DROP VIEW IF EXISTS v_sensor_current;
DROP VIEW IF EXISTS v_region_path;
