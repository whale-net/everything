-- Migration 015: v_sensor_last_reading / v_board_last_reading
--
-- Replaces the ListBoardsWithState query's unbounded
-- board -> sensor -> sensor_reading join + MAX(recorded_at) aggregate, which
-- scanned every row ever recorded in the sensor_reading hypertable on every
-- call to GET /boards.
--
-- v_sensor_last_reading finds each sensor's latest reading via a LATERAL
-- subquery with ORDER BY recorded_at DESC LIMIT 1, which the existing
-- idx_sensor_reading_sensor_id(sensor_id, recorded_at DESC) index answers in
-- O(1) per sensor instead of scanning the whole table.

CREATE VIEW v_sensor_last_reading AS
SELECT
    s.sensor_id,
    s.board_id,
    lr.recorded_at AS last_reading_at
FROM sensor s
LEFT JOIN LATERAL (
    SELECT sr.recorded_at
    FROM sensor_reading sr
    WHERE sr.sensor_id = s.sensor_id
    ORDER BY sr.recorded_at DESC
    LIMIT 1
) lr ON TRUE;

-- v_board_last_reading rolls v_sensor_last_reading up to one row per board:
-- the most recent reading across all of that board's sensors.

CREATE VIEW v_board_last_reading AS
SELECT
    b.board_id,
    b.device_id,
    MAX(slr.last_reading_at) AS last_reading_at
FROM board b
LEFT JOIN v_sensor_last_reading slr ON slr.board_id = b.board_id
GROUP BY b.board_id, b.device_id;
