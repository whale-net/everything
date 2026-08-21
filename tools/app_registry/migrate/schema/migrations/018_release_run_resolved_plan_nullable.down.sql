-- Rollback: restore resolved_plan NOT NULL. Will fail if any release_run
-- row currently has resolved_plan NULL (i.e. a release triggered under the
-- nullable-column fix whose ReleaseWorkflow hasn't reached
-- RecordResolvedPlan yet) -- same dev/CI-safety-valve caveat as 017's
-- down.sql: migration rollbacks in this repo are not a production
-- operation, a real rollback would need to reconcile those rows first.
ALTER TABLE release_run ALTER COLUMN resolved_plan SET NOT NULL;
