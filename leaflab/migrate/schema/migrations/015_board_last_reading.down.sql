-- Reverse migration 015: drop views in reverse dependency order.

DROP VIEW IF EXISTS v_board_last_reading;
DROP VIEW IF EXISTS v_sensor_last_reading;
