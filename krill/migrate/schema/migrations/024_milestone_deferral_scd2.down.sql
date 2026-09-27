-- Restores `milestone_deferral`'s pre-024 shape: the `id` primary key, no
-- `revision_id`/`valid_from`/`valid_to`, and 010's two indexes without
-- their `valid_to IS NULL` predicate.
--
-- Note what this does NOT reverse: `milestone_deferral.milestone_id`'s
-- referential action to milestone_ref, which migration 020 dropped and
-- migration 020's own down migration restores. This migration's down runs
-- first on the way back down, and the table it hands back is exactly the
-- post-020 / pre-024 shape 020's down expects.
--
-- Fails loudly, by design, if any deferral has been amended since: two
-- revisions then share one `id`, which the restored PRIMARY KEY cannot
-- hold. That is the correct posture for a reversible-schema boundary --
-- the operator decides what to do with those rows first rather than having
-- this migration silently collapse a lineage. Same posture, same reason,
-- as 020_milestone_scd2.down.sql.
DROP INDEX milestone_deferral_scope_idx;
DROP INDEX milestone_deferral_milestone_idx;
DROP INDEX milestone_deferral_current_id_idx;

CREATE INDEX milestone_deferral_milestone_idx ON milestone_deferral(milestone_id);
CREATE INDEX milestone_deferral_scope_idx ON milestone_deferral(scope_id);

ALTER TABLE milestone_deferral DROP CONSTRAINT milestone_deferral_pkey;
ALTER TABLE milestone_deferral ADD PRIMARY KEY (id);

ALTER TABLE milestone_deferral
    DROP COLUMN revision_id,
    DROP COLUMN valid_from,
    DROP COLUMN valid_to;
