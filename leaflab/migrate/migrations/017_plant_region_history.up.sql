-- Migration 017: plant_region_history schema, backfill and guards (FR19/FR21)
--
-- Picked 016 as the next free number after 015 (ownership); renumbered to
-- 017 in a later commit on this branch after colliding with a sibling v2
-- branch's audit_log migration -- sibling branches on plan/1166 have
-- collided on migration numbers more than once.
--
-- This is defect 1's fix: plant, plant_type and v_sensor_reading_with_plant
-- have existed since migration 001 and nothing writes them -- the view
-- joins p.region_id = e.region_id (current placement, exact equality), so
-- moving a plant re-attributes every reading it ever produced.
--
-- plant_region_history is the SCD2 record of truth for where a plant has
-- been over time, following AGENTS.md's valid_from/valid_to convention.
-- This migration carries the schema, the attribution-neutral snapped-to-hour
-- backfill (FR21) and the database-side no-back-dating guard (NFR6.2). The
-- application-level no-back-dating refusal and the close-and-open writer
-- (FR19) live in leaflab/api/placement -- this migration only guards direct
-- inserts. FR25/FR28/FR72 (the read-path repoint) land after this, per
-- NFR8's fixed ordering.
--
-- plant.region_id disposition: left in place as a current-value cache, not
-- dropped or repurposed here. It is kept in sync by
-- leaflab/api/placement.Writer.Move on every recorded move (mirroring the
-- board.household_id cache pattern from migration 015) so the pre-FR72 read
-- path (v_sensor_reading_with_plant, which still joins on plant.region_id)
-- keeps working unchanged through this phase. It is dropped only once FR72
-- repoints that view onto plant_region_history -- leaving it in place here
-- avoids a second breaking change mid-phase.

CREATE TABLE plant_region_history (
    plant_region_history_id BIGSERIAL PRIMARY KEY,
    plant_id           BIGINT NOT NULL REFERENCES plant(plant_id),
    region_id          BIGINT NOT NULL REFERENCES region(region_id),
    valid_from          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to            TIMESTAMPTZ,
    relocation_induced  BOOLEAN NOT NULL DEFAULT FALSE  -- FR24, written from Phase 5
);

-- Open-interval partial indexes, both directions (NFR6.1): plant-to-region
-- at T and region-to-plant at T are both hot paths for FR20's read path.
CREATE INDEX idx_plant_region_history_plant_id_current
    ON plant_region_history(plant_id) WHERE valid_to IS NULL;
CREATE INDEX idx_plant_region_history_region_id_current
    ON plant_region_history(region_id) WHERE valid_to IS NULL;

-- Temporal indexes serving AGENTS.md's value-at-time-T predicate, both
-- directions.
CREATE INDEX idx_plant_region_history_plant_id_temporal
    ON plant_region_history(plant_id, valid_from, valid_to);
CREATE INDEX idx_plant_region_history_region_id_temporal
    ON plant_region_history(region_id, valid_from, valid_to);

-- ── Backfill: plant.region_id → initial plant_region_history interval (FR21) ──
--
-- golang-migrate's postgres driver runs an entire *.up.sql file in one
-- transaction (libs/go/migrate wraps postgres.WithInstance), so the schema
-- above and this backfill land or roll back together -- "in one
-- transaction" per this task's Implementation section.
--
-- earliest_reading replicates v_sensor_reading_with_plant's exact join
-- predicate (migration 012) -- region_id equality plus the
-- created_at/removed_at window -- so "earliest reading currently
-- attributed to that plant" means exactly what today's read path already
-- attributes, not a looser region_id-only match.
--
-- valid_from is date_trunc('hour', ...) of that earliest attributed
-- reading, snapped outward (earlier): the opened interval always starts no
-- later than the earliest reading it must cover (FR21). A plant with no
-- attributed reading falls back to its own created_at, same hour-snap.
--
-- valid_to, for an already-removed plant (removed_at IS NOT NULL), is the
-- *end* of the hour bucket containing removed_at -- snapped outward, later
-- -- so no boundary lands mid-bucket. A still-present plant gets NULL
-- (open). This is the source of FR21's disclosed cost: a plant removed at
-- e.g. 14:20 gets valid_to = 15:00, so it and whatever plant next occupies
-- the region from 14:00 share the 14:00-15:00 bucket -- accepted and
-- documented, not a defect in this backfill.
--
-- relocation_induced is explicitly FALSE (also the column default) -- this
-- migration never sets it TRUE; Phase 5's FR74 is the only writer of TRUE.
WITH earliest_reading AS (
    SELECT
        p.plant_id,
        MIN(sr.recorded_at) AS earliest_recorded_at
    FROM plant p
    LEFT JOIN sensor_reading sr
           ON sr.region_id   = p.region_id
          AND p.created_at  <= sr.recorded_at
          AND (p.removed_at IS NULL OR p.removed_at > sr.recorded_at)
    GROUP BY p.plant_id
)
INSERT INTO plant_region_history (plant_id, region_id, valid_from, valid_to, relocation_induced)
SELECT
    p.plant_id,
    p.region_id,
    date_trunc('hour', COALESCE(er.earliest_recorded_at, p.created_at)) AS valid_from,
    CASE
        WHEN p.removed_at IS NOT NULL
            THEN date_trunc('hour', p.removed_at) + INTERVAL '1 hour'
        ELSE NULL
    END AS valid_to,
    FALSE
FROM plant p
JOIN earliest_reading er ON er.plant_id = p.plant_id;

-- ── No-back-dating guard, database side (NFR6.2) ─────────────────────────────
--
-- FR19: an interval opens at the instant the change is recorded, never
-- earlier. leaflab/api/placement.Writer.Move enforces the caller-facing half
-- (refusing a requested boundary earlier than the moment the request is
-- processed, via contract.Refuse per FR59.3, before anything is written).
-- This trigger is the same rule enforced close to the data (NFR6.2), so it
-- holds even for a direct INSERT that bypasses the Writer: valid_from can
-- never be recorded later than the instant Postgres executes the INSERT.
--
-- Implemented as a trigger, not a CHECK: CHECK constraints must be
-- IMMUTABLE and NOW() is only STABLE, so `CHECK (valid_from <= NOW())` is
-- rejected by Postgres at CREATE TABLE time. A BEFORE INSERT trigger gets
-- the same effect. Created after the backfill above so the trigger never
-- has to consider backfilled rows -- their valid_from values are historical
-- (always <= NOW() already) and would pass regardless.
CREATE FUNCTION enforce_plant_region_history_no_future_valid_from() RETURNS TRIGGER AS $$
BEGIN
    IF NEW.valid_from > NOW() THEN
        RAISE EXCEPTION 'plant_region_history.valid_from (%) cannot be later than now (%) -- an interval opens at the instant it is recorded (FR19, NFR6.2)',
            NEW.valid_from, NOW();
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_plant_region_history_no_future_valid_from
    BEFORE INSERT ON plant_region_history
    FOR EACH ROW
    EXECUTE FUNCTION enforce_plant_region_history_no_future_valid_from();
