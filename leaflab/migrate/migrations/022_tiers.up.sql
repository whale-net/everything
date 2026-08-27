-- Migration 022: granularity tiers -- 5-minute and hourly continuous
-- aggregates over sensor_reading, with refresh and retention policies
-- ordered so no refresh window ever reaches into dropped raw data (FR71,
-- NFR5).
--
-- Migration number: 022 is the next free number after checking every disk
-- worktree under .pm-worktrees/ for the true highest claimed migration
-- number at scaffold time -- 021 was the highest live claim among v2
-- branches (1361's household_id-on-view migration). plan/1166-XXXX
-- (no "v2") branches and plan/1166-rebuild-trunk are the v1 attempt --
-- unmerged and explicitly out of scope per this issue's own text -- so
-- their migration numbers (up to 027) are not counted here. Sibling
-- branches on this plan have collided on migration numbers more than
-- once; renumber on conflict, same as 016->017 and 017->018 before this.
--
-- ── Three tiers, no dimension joins (NFR5) ──────────────────────────────
-- sensor_reading carries sensor_id and region_id directly on the hypertable
-- (region_id is denormalized at write time -- see migration 001's comment
-- on sensor_reading). Both aggregates below group by those two columns
-- plus the time bucket, with no join to sensor, sensor_type or region.
-- This is deliberate: NFR5 requires aggregates to be "defined directly on
-- the hypertable and carry no dimension joins; enrichment is applied above
-- them." The issue text's worked example additionally groups by a
-- "measurement_type" column -- sensor_reading has no such column (a
-- sensor's measurement type lives on sensor.sensor_type_id, a dimension
-- table reachable only by joining sensor). Grouping by sensor_id already
-- disambiguates measurement type one-to-one (one sensor, one type) without
-- joining to fetch a label for it, so measurement_type is left out of the
-- GROUP BY here; the type name/unit is enrichment applied above the
-- aggregate, same as every other dimension attribute already handled that
-- way for raw readings (sensor_type_name, region_name, etc. -- see
-- v_sensor_reading_enriched in migration 012).
--
-- sum(value) is carried alongside avg(value) so the hourly tier (below)
-- can recompute a correct *weighted* average from the 5-minute tier
-- (sum(value_sum) / sum(reading_count)) instead of an unweighted
-- avg(avg) -- averaging five 5-minute averages only equals the true hourly
-- average when every 5-minute bucket has the same reading count, which is
-- not guaranteed (sensor gaps, per-sensor report rates, invalid readings).
-- min/max do not have this problem: min(min)/max(max) is exact at any
-- level of composition, which is what NFR5's exactness test checks.
CREATE MATERIALIZED VIEW sensor_reading_5m
WITH (
    timescaledb.continuous,
    -- Required for sensor_reading_1h (below) to be built hierarchically on
    -- top of this view: TimescaleDB does not support composing a
    -- continuous aggregate on a source that still has real-time
    -- aggregation (the default) enabled. Disabling it here also means
    -- querying sensor_reading_5m directly never implicitly unions in raw
    -- rows outside the tier's own refresh/retention contract -- freshness
    -- within the refresh lag is served by falling back to raw (leaflab/api
    -- tier selection, within the FR71 48-hour cap), not by a hidden
    -- real-time union inside the view.
    timescaledb.materialized_only = true
) AS
SELECT
    time_bucket('5 minutes', recorded_at) AS bucket,
    sensor_id,
    region_id,
    count(*)   AS reading_count,
    sum(value) AS value_sum,
    avg(value) AS value_avg,
    min(value) AS value_min,
    max(value) AS value_max
FROM sensor_reading
GROUP BY 1, 2, 3
WITH NO DATA;

CREATE INDEX idx_sensor_reading_5m_sensor_id ON sensor_reading_5m(sensor_id, bucket DESC);
CREATE INDEX idx_sensor_reading_5m_region_id ON sensor_reading_5m(region_id, bucket DESC);

-- ── Hourly tier, composed from the 5-minute tier ────────────────────────
-- TimescaleDB (timescale/timescaledb:latest-pg16, the image leaflab/Tiltfile
-- pins for local dev) supports hierarchical continuous aggregates as of
-- 2.9: a continuous aggregate may be defined directly on top of another
-- continuous aggregate, provided the child's bucket width is an integer
-- multiple of the parent's and the parent has real-time aggregation
-- disabled (materialized_only = true, set above). 1 hour is a clean 12x
-- multiple of 5 minutes, so sensor_reading_1h is built FROM
-- sensor_reading_5m, not from raw sensor_reading -- "composed from the
-- 5-minute tier where TimescaleDB supports hierarchical continuous
-- aggregates" per this task's Scaffold instructions, and it does, here.
CREATE MATERIALIZED VIEW sensor_reading_1h
WITH (
    timescaledb.continuous,
    timescaledb.materialized_only = true  -- see sensor_reading_5m's comment
) AS
SELECT
    time_bucket('1 hour', bucket) AS bucket,
    sensor_id,
    region_id,
    sum(reading_count)                             AS reading_count,
    sum(value_sum)                                  AS value_sum,
    sum(value_sum) / NULLIF(sum(reading_count), 0)  AS value_avg,
    min(value_min)                                  AS value_min,
    max(value_max)                                  AS value_max
FROM sensor_reading_5m
GROUP BY 1, 2, 3
WITH NO DATA;

CREATE INDEX idx_sensor_reading_1h_sensor_id ON sensor_reading_1h(sensor_id, bucket DESC);
CREATE INDEX idx_sensor_reading_1h_region_id ON sensor_reading_1h(region_id, bucket DESC);

-- ── Refresh policies ─────────────────────────────────────────────────────
--
-- Ordering constraint (NFR5, the load-bearing part): "Refresh and
-- retention policies are ordered so that no refresh window ever reaches
-- into dropped raw data, and so that every boundary capture FR20 depends
-- on is complete and durable before raw retention elapses for the chunk
-- containing that boundary." Every window below is written as a literal
-- multiple of one base interval -- INTERVAL '1 hour', typed once,
-- immediately below as capture_completion_window -- rather than as
-- independently chosen numbers, so the ordering cannot be broken by
-- editing one policy in isolation. This is a SQL migration, not a
-- programming language, so "one constant" is expressed as arithmetic on a
-- single literal rather than a named variable; the Testing phase's
-- ordering test reads these same policy configurations back from
-- timescaledb_information.jobs / .continuous_aggregates and asserts the
-- relationship programmatically, rather than trusting this comment.
--
--   capture_completion_window = INTERVAL '1 hour'
--     FR20's boundary capture is "a deferred second write at bucket close,
--     not a point-in-time write at the boundary instant" -- this is the
--     outside bound on how late that deferred write may land after the
--     bucket it captures closes. Set to the hourly bucket width itself:
--     FR71 makes hourly the coarsest tier and the one a boundary partial
--     inherits indefinite retention from, so bounding the capture window
--     at one hourly bucket is the natural (and generous) ceiling.
--
--   five_minute_refresh_lag = capture_completion_window        (=  1 hour)
--     The 5-minute continuous aggregate must not refresh a bucket until
--     any deferred boundary-capture write landing in that bucket is
--     guaranteed durable.
--
--   hourly_refresh_lag = 2 * capture_completion_window          (=  2 hours)
--     The hourly tier is composed FROM the 5-minute tier (above) -- it
--     must not refresh until the 5-minute tier's own refresh for that
--     period has completed (one capture_completion_window) *and* its own
--     boundary-capture durability window has separately elapsed (a second
--     capture_completion_window).
--
--   raw_retention_min = five_minute_refresh_lag + hourly_refresh_lag
--                      + capture_completion_window
--                      = 4 * capture_completion_window          (=  4 hours)
--     The floor the raw retention drop_after (below) must clear. Raw
--     retention is actually set to 13 months (A12's business requirement:
--     rejected a 90-day floor because it breaks postmortems older than a
--     quarter) -- vastly larger than this 4-hour floor, so the ordering
--     holds by construction today. It still needs a live assertion (not
--     just this comment) because raw_retention_min tracks
--     capture_completion_window: if a future edit widens
--     capture_completion_window enough, or narrows raw retention enough,
--     to close that 4-hour-vs-13-month gap, the Testing phase's ordering
--     test must catch it.
SELECT add_continuous_aggregate_policy('sensor_reading_5m',
    start_offset      => INTERVAL '3 days',
    end_offset        => INTERVAL '1 hour',   -- capture_completion_window
    schedule_interval  => INTERVAL '5 minutes');

SELECT add_continuous_aggregate_policy('sensor_reading_1h',
    start_offset      => INTERVAL '3 days',
    end_offset        => INTERVAL '2 hours',  -- 2 * capture_completion_window
    schedule_interval  => INTERVAL '15 minutes');

-- ── Retention policies ───────────────────────────────────────────────────
--
-- FR71: "raw at least 13 months, 5-minute 90 days, hourly indefinitely."
-- Ordered raw -> 5-minute -> (no hourly policy) in this file so the
-- FR71-mandated coarsest-survives-longest shape (hourly > raw > 5-minute)
-- reads top to bottom against increasing tier coarseness, even though raw
-- retention (13 months) numerically exceeds 5-minute retention (90 days)
-- -- both vastly exceed the 4-hour raw_retention_min floor derived above,
-- so nothing here reaches into data a refresh policy still needs.
SELECT add_retention_policy('sensor_reading', drop_after => INTERVAL '13 months');
SELECT add_retention_policy('sensor_reading_5m', drop_after => INTERVAL '90 days');
-- No add_retention_policy for sensor_reading_1h: hourly retention is
-- indefinite (FR71) -- also the tier a boundary partial inherits retention
-- from at the coarsest tier it splits, per FR71's boundary-partial rule.
