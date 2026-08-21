-- Rollback: restore the blanket UNIQUE index on temporal_workflow_id
-- (issue #889). NOTE: this will fail to apply if any two release_run rows
-- already share a temporal_workflow_id (i.e. once the .up.sql migration
-- has allowed a legitimate re-release to be created) -- a real rollback
-- would need to reconcile those rows first; not attempted here since
-- schema/migrations rollbacks in this repo are a dev/CI safety valve, not
-- a production operation (see docs conventions).
DROP INDEX IF EXISTS release_run_temporal_workflow_id_idx;
CREATE UNIQUE INDEX release_run_temporal_workflow_id_idx ON release_run (temporal_workflow_id);
