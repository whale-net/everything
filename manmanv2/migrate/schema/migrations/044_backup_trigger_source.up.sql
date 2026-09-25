-- Migration 044: backups.trigger_source (M7 FR2, plan #2777)
--
-- Records whether a backup row was created by the cadence-driven scheduler
-- or a manual trigger, so the backup surface (FR3) can display it. The
-- DEFAULT 'unknown' backfills every pre-existing row to 'unknown' -- FR2 is
-- explicit that history must never be guessed into 'manual' or 'scheduled'.
ALTER TABLE backups
    ADD COLUMN IF NOT EXISTS trigger_source TEXT NOT NULL DEFAULT 'unknown'
        CHECK (trigger_source IN ('scheduled', 'manual', 'unknown'));
