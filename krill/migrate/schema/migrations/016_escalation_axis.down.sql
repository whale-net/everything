-- Reverses 016_escalation_axis in FK-safe (reverse-creation) order and
-- restores 015's original index/CHECK shapes exactly -- down-migrating
-- with live escalation/intervention/note-lifecycle rows, or any escalated
-- or cancelled task, present drops them along with the schema support,
-- the same "down-migrations don't backfill or preserve data" posture
-- 015's own down migration takes.

-- Restore 015's original single-predicate claimable index before
-- touching the columns it depends on.
DROP INDEX IF EXISTS task_claimable_idx;
CREATE INDEX task_claimable_idx ON task(scope_id) WHERE current_claim_id IS NULL;

-- Restore task_attempt.outcome and task_claim.release_reason to their
-- 015-shipped CHECKs.
ALTER TABLE task_attempt
    DROP CONSTRAINT task_attempt_outcome_check,
    ADD CONSTRAINT task_attempt_outcome_check CHECK (outcome IN ('claimed', 'lapsed', 'abandoned', 'completed'));

ALTER TABLE task_claim
    DROP CONSTRAINT task_claim_release_reason_check,
    ADD CONSTRAINT task_claim_release_reason_check CHECK (release_reason IN ('complete', 'abandon', 'reclaim'));

ALTER TABLE task_note
    DROP COLUMN current_status;

ALTER TABLE task
    DROP COLUMN thrash_count,
    DROP COLUMN current_escalation_id,
    DROP COLUMN cancelled_at;

-- task_intervention_event references task_escalation_event, so it must
-- drop first; task_note_lifecycle_event only references task_note, which
-- this migration never drops.
DROP TABLE IF EXISTS task_intervention_event;
DROP TABLE IF EXISTS task_escalation_event;
DROP TABLE IF EXISTS task_note_lifecycle_event;
