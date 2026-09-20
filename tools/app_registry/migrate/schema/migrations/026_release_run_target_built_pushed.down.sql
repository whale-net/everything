-- Rollback release_run_target's built/pushed states (see .up.sql doc comment).
ALTER TABLE release_run_target DROP CONSTRAINT release_run_target_state_check;
ALTER TABLE release_run_target ADD CONSTRAINT release_run_target_state_check CHECK (
    state IN ('queued', 'building', 'publishing', 'recording', 'succeeded', 'failed')
);
