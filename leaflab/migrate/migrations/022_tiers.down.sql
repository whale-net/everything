-- Migration 022 down: reverse the granularity tiers -- drop retention
-- policies, drop refresh policies, drop both continuous aggregates.
-- sensor_reading_1h is dropped before sensor_reading_5m since it is built
-- hierarchically on top of the 5-minute tier (up.sql) and depends on it.

SELECT remove_retention_policy('sensor_reading_5m', if_exists => true);
SELECT remove_retention_policy('sensor_reading', if_exists => true);

SELECT remove_continuous_aggregate_policy('sensor_reading_1h', if_exists => true);
SELECT remove_continuous_aggregate_policy('sensor_reading_5m', if_exists => true);

DROP MATERIALIZED VIEW IF EXISTS sensor_reading_1h;
DROP MATERIALIZED VIEW IF EXISTS sensor_reading_5m;
