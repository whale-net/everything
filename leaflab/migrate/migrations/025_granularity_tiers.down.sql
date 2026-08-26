-- Rollback: drop granularity tier objects in reverse dependency order.

DROP FUNCTION IF EXISTS verify_tier_policy_ordering();

SELECT delete_job(job_id) FROM timescaledb_information.jobs
WHERE proc_name = 'enforce_raw_retention';

DROP PROCEDURE IF EXISTS enforce_raw_retention(INT, JSONB);
DROP FUNCTION IF EXISTS raw_retention_captures_complete(TIMESTAMPTZ);

SELECT remove_retention_policy('sensor_reading_5m', if_exists => TRUE);
SELECT remove_continuous_aggregate_policy('sensor_reading_1h', if_exists => TRUE);
SELECT remove_continuous_aggregate_policy('sensor_reading_5m', if_exists => TRUE);

DROP MATERIALIZED VIEW IF EXISTS sensor_reading_1h;
DROP MATERIALIZED VIEW IF EXISTS sensor_reading_5m;
