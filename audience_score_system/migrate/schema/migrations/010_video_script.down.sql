-- Reverse migration 010. Does not restore backfilled/dropped data --
-- matching migration 002's down convention (no data-preservation
-- attempted).

DROP INDEX IF EXISTS video_schedule_match_video_script_id;
ALTER TABLE video_schedule_match DROP COLUMN IF EXISTS video_script_id;

DROP TABLE IF EXISTS video_script;
