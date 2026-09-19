-- Drops every work-axis table in FK-safe (reverse-creation) order --
-- task_note and task_attempt and task_lease_event each reference
-- task_claim and/or task, task_claim references task, and task_dependency
-- references task twice, so task itself must be dropped last. Down-
-- migrating with live tasks/claims/leases/attempts/notes present drops
-- them along with the tables -- the same "down-migrations don't backfill
-- or preserve data" posture every other migration in this package takes
-- (011_milepebble.down.sql, 012_milestone_status.down.sql): a schema that
-- has gone this far forward is not expected to cleanly reverse in
-- production. `krill_session` (003_session) and `milestone_ref`
-- (004_milestone_assoc/010/011/014) are both left completely untouched --
-- this migration only ever added REFERENCES onto them, never a column to
-- either table itself.
DROP TABLE IF EXISTS task_note;
DROP TABLE IF EXISTS task_attempt;
DROP TABLE IF EXISTS task_lease_event;
DROP TABLE IF EXISTS task_claim;
DROP TABLE IF EXISTS task_dependency;
DROP TABLE IF EXISTS task;
