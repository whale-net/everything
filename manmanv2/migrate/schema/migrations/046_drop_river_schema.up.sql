-- Migration 046: drop the River scheduler's schema (M7 NFR2, plan #2777,
-- task #2819) -- hard cutover now that the Temporal-based scheduler
-- (backupsched, migration-independent) is the sole backup scheduler.
--
-- Every object here is one `rivermigrate.Migrate` (river/riverdriver/
-- riverpgxv5's "main" migration line, versions 001-006) created at
-- manmanv2/processor's old startBackupScheduler startup path. IF EXISTS
-- guards make this a no-op on a database that never ran River (e.g. a
-- fresh environment stood up after this migration already landed).
--
-- river_client_queue has an ON DELETE CASCADE FK to river_client, and
-- river_job/river_leader/river_queue/river_migration are unreferenced by
-- anything else in this schema, so dropping each table CASCADE also takes
-- its own indexes, triggers and constraints with it. The two remaining
-- non-table objects -- the river_job_state_in_bitmask() function and the
-- river_job_state enum it's typed against -- are dropped explicitly,
-- function first since it references the enum.
DROP TABLE IF EXISTS river_client_queue CASCADE;
DROP TABLE IF EXISTS river_client CASCADE;
DROP TABLE IF EXISTS river_queue CASCADE;
DROP TABLE IF EXISTS river_leader CASCADE;
DROP TABLE IF EXISTS river_job CASCADE;
DROP TABLE IF EXISTS river_migration CASCADE;

DROP FUNCTION IF EXISTS river_job_state_in_bitmask(BIT(8), river_job_state);
DROP TYPE IF EXISTS river_job_state;
