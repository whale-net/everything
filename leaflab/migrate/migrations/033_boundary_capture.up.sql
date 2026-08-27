-- Migration 033: two-phase boundary capture schema (FR20, A14, A17, NFR5)
--
-- Migration number: 033 is the next free number after checking every disk
-- worktree under .pm-worktrees/ for the true highest claimed migration
-- number at scaffold time -- 032 was the highest live claim among v2
-- branches (1346's support_reference migration and 1373's
-- ack_write_guard migration both claim 032; every other live v2 worktree
-- at scaffold time is lower). plan/1166-XXXX (no "v2") branches and
-- plan/1166-rebuild-trunk are the v1 attempt -- unmerged and explicitly
-- out of scope per this issue's own text -- so their migration numbers are
-- not counted here. Sibling branches on this plan have collided on
-- migration numbers more than once (see 022_tiers.up.sql's own note);
-- renumber on conflict, same as 016->017 and 017->018 before that.
--
-- boundary_capture and boundary_partial are the durable record FR20's
-- two-phase capture depends on: a bucket a plant moved through is resolved
-- exactly for the life of the tier that holds it, including after raw rows
-- have aged out (FR20.2), by capturing the affected sensors and straddled
-- buckets at the instant a placement boundary is recorded (phase one,
-- leaflab/api/capture.Recorder, scaffolded alongside this migration), then
-- computing each straddling bucket's exact partials from raw -- both
-- sides, never by subtraction (A17) -- when the bucket closes (phase two,
-- leaflab/api/capture.Completer).
--
-- Keyed by sensor_id and instant, never by plant_id (FR20.2: "the capture
-- is keyed by sensor and instant, never by plant"). Attribution is
-- resolved above the aggregate at read time (the read-path task that
-- follows this one), never baked into the capture itself -- this is also
-- why neither table below references plant or plant_region_history.

-- ── boundary_capture: phase-one record of an affected sensor/tier/bucket ──
--
-- One row per (sensor, tier) pair whose bucket straddles a placement
-- boundary, inserted by leaflab/api/capture.Recorder in the same
-- transaction as the placement write (FR20's Implementation section).
-- state starts 'pending' and moves to 'completed' once
-- leaflab/api/capture.Completer has computed and durably written that
-- capture's partials at bucket close (phase two, A17).
CREATE TABLE boundary_capture (
    capture_id    BIGSERIAL PRIMARY KEY,
    sensor_id     BIGINT NOT NULL REFERENCES sensor(sensor_id) ON DELETE RESTRICT,
    boundary_at   TIMESTAMPTZ NOT NULL,
    tier          TEXT NOT NULL,
    bucket_start  TIMESTAMPTZ NOT NULL,
    state         TEXT NOT NULL DEFAULT 'pending',
    completed_at  TIMESTAMPTZ,
    CONSTRAINT boundary_capture_tier_check
        CHECK (tier IN ('five_minute', 'hourly')),
    CONSTRAINT boundary_capture_state_check
        CHECK (state IN ('pending', 'completed')),
    CONSTRAINT boundary_capture_completed_at_check
        CHECK ((state = 'completed') = (completed_at IS NOT NULL))
);

-- Phase two's hot path (leaflab/api/capture.Completer.RunPending): find
-- every capture whose bucket has closed and is still pending, per tier.
-- Partial index since 'pending' is the transient minority state once the
-- completer has caught up -- also the index NFR5's "still pending as its
-- raw chunk nears retention" alert scans.
CREATE INDEX idx_boundary_capture_pending
    ON boundary_capture(tier, bucket_start)
    WHERE state = 'pending';

-- Sensor-scoped lookup -- both the read path's per-sensor capture history
-- and ad hoc debugging of a single sensor's captures.
CREATE INDEX idx_boundary_capture_sensor_id
    ON boundary_capture(sensor_id, boundary_at);

-- ── boundary_partial: the exact split-bucket aggregates ─────────────────
--
-- One row per [partial_from, partial_to) sub-interval of a straddled
-- bucket, at the tier the bucket belongs to (FR20.3: "N boundaries in one
-- bucket compose to N+1 partials" -- a new boundary splits the existing
-- partial it falls inside into two, so N boundaries always yield exactly
-- N+1 rows here for that bucket/tier).
--
-- No measurement_type column: migration 022 already established that
-- sensor_id alone disambiguates measurement type one-to-one for this
-- schema (a sensor has exactly one sensor_type_id) without joining a
-- dimension table to fetch it -- the same reasoning applies here, since
-- every boundary_partial traces back to exactly one sensor_id via
-- capture_id -> boundary_capture.sensor_id.
--
-- min/max are carried as their own columns, computed independently on
-- each side of every boundary and never derived from the other side or
-- from the full bucket (FR20.2: "min and max are not invertible"; A17).
-- reading_count/value_sum/value_min/value_max mirror migration 022's
-- aggregate column names so a caller composing tiers (hourly partials
-- from 5-minute partials, FR20.3's "a coarser tier's partials are
-- composed from the finer tier's") can treat a partial and a full
-- continuous-aggregate bucket identically wherever only these four
-- aggregates are needed -- value_avg is never stored, same as migration
-- 022: it is always derived as value_sum / reading_count, never averaged.
CREATE TABLE boundary_partial (
    partial_id    BIGSERIAL PRIMARY KEY,
    capture_id    BIGINT NOT NULL REFERENCES boundary_capture(capture_id) ON DELETE RESTRICT,
    tier          TEXT NOT NULL,
    bucket_start  TIMESTAMPTZ NOT NULL,
    partial_from  TIMESTAMPTZ NOT NULL,
    partial_to    TIMESTAMPTZ NOT NULL,
    reading_count BIGINT NOT NULL,
    value_sum     DOUBLE PRECISION NOT NULL,
    value_min     DOUBLE PRECISION NOT NULL,
    value_max     DOUBLE PRECISION NOT NULL,
    CONSTRAINT boundary_partial_tier_check
        CHECK (tier IN ('five_minute', 'hourly')),
    CONSTRAINT boundary_partial_interval_check
        CHECK (partial_from < partial_to)
);

CREATE INDEX idx_boundary_partial_capture_id ON boundary_partial(capture_id);

-- Read path's hot lookup (next task): given a tier and a straddled bucket,
-- find its partials in partial_from order.
CREATE INDEX idx_boundary_partial_bucket
    ON boundary_partial(tier, bucket_start, partial_from);

-- ── Retention (FR20.2, FR71) ─────────────────────────────────────────────
--
-- "Retention on boundary_partial follows the coarsest tier the partial
-- splits -- hourly partials are never dropped." boundary_partial is
-- deliberately not a TimescaleDB hypertable here: capture volume is
-- bounded by how often plants move, not by reading volume, so it does not
-- need chunked time-partitioning the way sensor_reading does. That also
-- means tier-differentiated retention cannot be expressed as a single
-- add_retention_policy the way migration 022's raw/5-minute tiers are --
-- a hypertable retention policy drops whole chunks by time, regardless of
-- a row's tier value within the chunk, which would drop hourly partials
-- right alongside five_minute ones. Differential retention is therefore a
-- row-scoped scheduled DELETE (`WHERE tier = 'five_minute' AND
-- bucket_start < NOW() - INTERVAL '90 days'`, mirroring migration 022's
-- sensor_reading_5m retention window, and never touching tier = 'hourly'
-- rows) -- this task's Implementation phase wires the job; no retention
-- policy or scheduled job is created by this migration.
