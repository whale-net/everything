-- Down-migrating drops the whole append-only history along with the
-- table -- there is no "downgrade a milestone/milepebble to statusless"
-- behavior to preserve beyond that: a `milestone_ref` row itself is left
-- completely untouched (this migration never altered that table), same
-- posture as every other migration in this package on its own Down().
DROP INDEX IF EXISTS milestone_status_event_milestone_created_idx;
DROP TABLE IF EXISTS milestone_status_event;
