-- Rollback release_run audit trail (issue #887)

DROP TABLE IF EXISTS release_run_target;
DROP TABLE IF EXISTS release_run;
