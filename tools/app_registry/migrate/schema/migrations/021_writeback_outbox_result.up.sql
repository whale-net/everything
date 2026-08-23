-- App Registry — writeback_outbox result columns (FR7a, issue #1029)
--
-- Adds `location`/`commit_sha` to `writeback_outbox`: what
-- GitOpsActivities.Publish actually produced, distinct from `workflow_id`/
-- `completed_at` (set by worker/outbox/drain.go's MarkDone when the
-- WritebackWorkflow is merely STARTED, not when its Publish activity
-- COMPLETES). Populated by RecordWritebackResult
-- (worker/writeback/record.go), called by WritebackWorkflow immediately
-- after Publish succeeds -- see workflow.go's PublishResult.CommitSHA doc
-- comment.
--
-- `commit_sha` is empty ('') on the no-op Skipped path (nothing new was
-- committed) and on StubActivities' no-git dev/test path (no real commit
-- ever happens there) -- never a stand-in/synthetic value. This is a plain
-- git SHA / relative file path, never a secret -- see GitOpsConfig's own
-- "never hardcode/log a credential" convention, which does not apply here.
ALTER TABLE writeback_outbox
    ADD COLUMN location   TEXT NOT NULL DEFAULT '',
    ADD COLUMN commit_sha TEXT NOT NULL DEFAULT '';
