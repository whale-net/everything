-- Retire schedule_entry and pacing_policy (FR41/FR45/FR47's schema half,
-- issue #1835). This lands last among the M2.1 code tasks -- every reader
-- was retargeted by #1829-#1834 before this drops the tables out from
-- under them. video_script (migration 010) is the sole surviving record
-- of a proposed video; v_prediction_vs_outcome was already re-anchored
-- off schedule_entry by migration 012.
--
-- Numbered 013: the plan assigned 013 to this migration, and 011/012 were
-- both already claimed (by #1833 and #1830/#1849 respectively) by the
-- time this task landed, so 013 is in fact the next free number --
-- preserving relative order after both.
--
-- Order matters for FK dependencies: the video_schedule_match index and
-- column referencing schedule_entry must go first, then schedule_entry,
-- then pacing_policy (no other table references it). No CASCADE anywhere
-- below -- if anything else still depends on either table, that is a
-- missed dependency upstream and this migration must fail loudly rather
-- than silently drop past it.

DROP INDEX IF EXISTS video_schedule_match_schedule_entry_id;
ALTER TABLE video_schedule_match DROP COLUMN schedule_entry_id;
DROP TABLE schedule_entry;
DROP TABLE pacing_policy;
