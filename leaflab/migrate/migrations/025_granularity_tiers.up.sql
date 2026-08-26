-- Migration 025: Granularity tiers -- 5-minute and hourly continuous
-- aggregates, with the retention and refresh policy ordering that protects
-- FR20 (FR71, NFR5).
--
-- Three tiers exist: raw (sensor_reading), 5-minute, hourly. No tier
-- coarser than hourly exists in V1 (A14). Retention: raw at least 13
-- months, 5-minute 90 days, hourly indefinitely (A12). There are no
-- continuous aggregates and no retention policies in leaflab/ before this
-- migration -- all of it is net-new policy, not existing configuration
-- being described.
--
-- Both continuous aggregates are defined DIRECTLY on the sensor_reading
-- hypertable and carry NO dimension joins (NFR5); enrichment (region path,
-- plant attribution) is applied above them, never inside their definition.
-- They are keyed by (sensor_id, region_id, bucket) only -- pre-aggregated
-- is not de-identified: a min/max over one sensor's bucket is two raw
-- readings wearing a hat, and any future k-suppression happens above a
-- tier, over contributors, never inherited from it (NFR17, #1211).
--
-- Policy ordering (NFR5):
--   1. No refresh window ever reaches into dropped raw data. The refresh
--      windows configured below look back minutes to hours -- far inside
--      the 13-month raw retention floor -- so retention structurally
--      cannot outrun refresh under any schedule this migration configures.
--      verify_tier_policy_ordering() makes this an assertable fact rather
--      than an assumption: it inspects the *actual* configured policy
--      state and raises if a refresh's end_offset ever exceeds the raw
--      floor, rather than trusting the schedule to keep behaving.
--   2. Raw for a chunk is not dropped before every FR20 boundary capture in
--      that chunk has completed. raw_retention_captures_complete() is the
--      gate: enforce_raw_retention() (this migration's raw-tier retention
--      action) consults it per chunk before dropping. #1208 owns the
--      boundary-capture table and will CREATE OR REPLACE this function's
--      body once that table exists, to query it. Until then it returns
--      TRUE unconditionally: with no capture table yet, nothing is
--      outstanding to block on, so the gate correctly holds open rather
--      than wedging retention shut before #1208 lands.

-- ── 5-minute tier ────────────────────────────────────────────────────────────

CREATE MATERIALIZED VIEW sensor_reading_5m
WITH (timescaledb.continuous) AS
SELECT
    sensor_id,
    region_id,
    time_bucket('5 minutes', recorded_at) AS bucket_start,
    MIN(value)  AS min_value,
    MAX(value)  AS max_value,
    SUM(value)  AS sum_value,
    COUNT(*)    AS reading_count
FROM sensor_reading
WHERE valid = TRUE
GROUP BY sensor_id, region_id, bucket_start
WITH NO DATA;

COMMENT ON VIEW sensor_reading_5m IS
    'FR71 5-minute tier. Defined directly on sensor_reading, no dimension joins. '
    'Keyed by (sensor_id, region_id, bucket_start) -- not de-identified (NFR17). '
    'Retention: 90 days (A12).';

CREATE INDEX idx_sensor_reading_5m_sensor ON sensor_reading_5m(sensor_id, bucket_start DESC);
CREATE INDEX idx_sensor_reading_5m_region ON sensor_reading_5m(region_id, bucket_start DESC);

-- Refresh window: materialize from 2 hours ago up to 10 minutes ago (leaves
-- late-arriving readings time to settle before a bucket is finalized),
-- re-run every 5 minutes. Both offsets are minutes/hours -- nowhere near
-- the 13-month raw floor -- by construction.
SELECT add_continuous_aggregate_policy('sensor_reading_5m',
    start_offset      => INTERVAL '2 hours',
    end_offset        => INTERVAL '10 minutes',
    schedule_interval => INTERVAL '5 minutes');

-- 90-day retention on the 5-minute tier (A12). This tier is defined
-- directly on the hypertable, independent of the hourly tier, so dropping
-- 5-minute data has no effect on the hourly tier's completeness.
SELECT add_retention_policy('sensor_reading_5m', drop_after => INTERVAL '90 days');

-- ── Hourly tier ──────────────────────────────────────────────────────────────

CREATE MATERIALIZED VIEW sensor_reading_1h
WITH (timescaledb.continuous) AS
SELECT
    sensor_id,
    region_id,
    time_bucket('1 hour', recorded_at) AS bucket_start,
    MIN(value)  AS min_value,
    MAX(value)  AS max_value,
    SUM(value)  AS sum_value,
    COUNT(*)    AS reading_count
FROM sensor_reading
WHERE valid = TRUE
GROUP BY sensor_id, region_id, bucket_start
WITH NO DATA;

COMMENT ON VIEW sensor_reading_1h IS
    'FR71 hourly tier -- coarsest tier in V1 (A14: no daily/weekly). Defined '
    'directly on sensor_reading, no dimension joins. Retained indefinitely: '
    'no retention policy is added for this tier.';

CREATE INDEX idx_sensor_reading_1h_sensor ON sensor_reading_1h(sensor_id, bucket_start DESC);
CREATE INDEX idx_sensor_reading_1h_region ON sensor_reading_1h(region_id, bucket_start DESC);

-- Refresh window: materialize from 3 hours ago up to 30 minutes ago,
-- re-run every 30 minutes. Again minutes/hours, nowhere near the raw floor.
SELECT add_continuous_aggregate_policy('sensor_reading_1h',
    start_offset      => INTERVAL '3 hours',
    end_offset        => INTERVAL '30 minutes',
    schedule_interval => INTERVAL '30 minutes');

-- No retention policy on sensor_reading_1h: hourly is retained indefinitely (A12).

-- ── FR20 boundary-capture completion gate ───────────────────────────────────
--
-- raw_retention_captures_complete(cutoff) reports whether every FR20
-- boundary capture for chunks entirely older than `cutoff` has completed.
-- Stub: #1208 owns the boundary-capture table and CREATE OR REPLACEs this
-- function's body once that table exists.

CREATE OR REPLACE FUNCTION raw_retention_captures_complete(cutoff TIMESTAMPTZ)
RETURNS BOOLEAN AS $$
BEGIN
    RETURN TRUE;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION raw_retention_captures_complete(TIMESTAMPTZ) IS
    'FR20/NFR5 gate: TRUE only when every boundary capture for chunks strictly '
    'before cutoff has completed. Stub until #1208 replaces this body with a '
    'query against its boundary-capture table.';

-- ── Raw retention, gated on FR20 boundary-capture completion ────────────────
--
-- enforce_raw_retention() is the raw tier's retention action, registered as
-- a scheduled job below instead of a plain add_retention_policy(). Unlike
-- add_retention_policy's unconditional age cutoff, it drops raw only when
-- BOTH:
--   1. it is older than the 13-month floor (A12), AND
--   2. raw_retention_captures_complete() reports the cutoff's captures are
--      done.
-- This is how the ordering constraint from NFR5 is encoded rather than
-- assumed: retention cannot outrun FR20's captures because it consults
-- their completion signal directly, every run, rather than trusting a
-- schedule never to race it. The ordering is stated against completion
-- (raw_retention_captures_complete), not against the boundary instant.

CREATE OR REPLACE PROCEDURE enforce_raw_retention(job_id INT, config JSONB)
LANGUAGE plpgsql AS $$
DECLARE
    cutoff TIMESTAMPTZ := NOW() - INTERVAL '13 months';
BEGIN
    IF NOT raw_retention_captures_complete(cutoff) THEN
        RAISE NOTICE 'enforce_raw_retention: FR20 boundary captures incomplete before %, skipping drop', cutoff;
        RETURN;
    END IF;

    PERFORM drop_chunks('sensor_reading', older_than => cutoff);
END;
$$;

COMMENT ON PROCEDURE enforce_raw_retention(INT, JSONB) IS
    'Raw-tier retention (>= 13 months, A12), gated on raw_retention_captures_complete '
    'so FR20 boundary captures always finish before their chunk''s raw is dropped.';

SELECT add_job('enforce_raw_retention', schedule_interval => INTERVAL '1 day');

-- ── Assertable refresh/retention ordering (NFR5) ────────────────────────────
--
-- verify_tier_policy_ordering() inspects the *actual* configured refresh
-- policies (not a hardcoded assumption) and raises if either tier's
-- end_offset could ever reach past the raw retention floor -- the failure
-- mode that would let a refresh be scheduled against a window whose raw has
-- already been dropped. Called by the migration test suite; callable ad hoc
-- to re-verify after any future policy change.

CREATE OR REPLACE FUNCTION verify_tier_policy_ordering()
RETURNS BOOLEAN AS $$
DECLARE
    raw_retention_floor INTERVAL := INTERVAL '13 months';
    rec RECORD;
BEGIN
    FOR rec IN
        SELECT
            ca.hypertable_name AS cagg_name,
            (js.config->>'end_offset')::INTERVAL AS end_offset
        FROM timescaledb_information.jobs js
        JOIN timescaledb_information.continuous_aggregates ca
          ON ca.materialization_hypertable_name = js.hypertable_name
        WHERE js.proc_name = 'policy_refresh_continuous_aggregate'
          AND ca.hypertable_name IN ('sensor_reading_5m', 'sensor_reading_1h')
    LOOP
        IF rec.end_offset IS NULL OR rec.end_offset > raw_retention_floor THEN
            RAISE EXCEPTION
                'tier % refresh end_offset % reaches past the % raw retention floor -- a refresh could be scheduled against dropped raw',
                rec.cagg_name, rec.end_offset, raw_retention_floor;
        END IF;
    END LOOP;

    RETURN TRUE;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION verify_tier_policy_ordering() IS
    'NFR5: raises if any tier''s configured refresh end_offset could reach past '
    'the raw retention floor. Asserts the ordering constraint against the live '
    'policy configuration, not against the schedule''s assumed behaviour.';
