-- Restores `milestone_ref`'s pre-SCD2 shape: the `id` primary key, no
-- `revision_id`/`valid_from`/`valid_to`, the five child referential
-- actions, and the three unique indexes without their `valid_to IS NULL`
-- predicate.
--
-- Fails loudly, by design, if any milestone has been amended since: two
-- revisions then share one `id`, which the restored PRIMARY KEY cannot
-- hold. That is the correct posture for a reversible-schema boundary --
-- the operator decides what to do with those rows first rather than having
-- this migration silently collapse a lineage.
DROP INDEX milestone_ref_current_id_idx;
DROP INDEX milestone_ref_parent_milestone_idx;
DROP INDEX milestone_ref_backlog_product_idx;
DROP INDEX milestone_ref_milepebble_parent_name_idx;
DROP INDEX milestone_ref_scope_product_name_idx;

CREATE UNIQUE INDEX milestone_ref_scope_product_name_idx
    ON milestone_ref(scope_id, product_id, name)
    WHERE parent_milestone_id IS NULL;

CREATE UNIQUE INDEX milestone_ref_milepebble_parent_name_idx
    ON milestone_ref(scope_id, product_id, parent_milestone_id, name)
    WHERE parent_milestone_id IS NOT NULL;

CREATE UNIQUE INDEX milestone_ref_backlog_product_idx
    ON milestone_ref(scope_id, product_id)
    WHERE kind = 'backlog';

CREATE INDEX milestone_ref_parent_milestone_idx ON milestone_ref(parent_milestone_id);

ALTER TABLE milestone_ref DROP CONSTRAINT milestone_ref_pkey;
ALTER TABLE milestone_ref ADD PRIMARY KEY (id);

ALTER TABLE milestone_ref            ADD CONSTRAINT milestone_ref_parent_milestone_id_fkey
    FOREIGN KEY (parent_milestone_id) REFERENCES milestone_ref(id);
ALTER TABLE task                     ADD CONSTRAINT task_milestone_id_fkey
    FOREIGN KEY (milestone_id) REFERENCES milestone_ref(id);
ALTER TABLE delivery_shipment        ADD CONSTRAINT delivery_shipment_milestone_id_fkey
    FOREIGN KEY (milestone_id) REFERENCES milestone_ref(id);
ALTER TABLE milestone_status_event   ADD CONSTRAINT milestone_status_event_milestone_id_fkey
    FOREIGN KEY (milestone_id) REFERENCES milestone_ref(id);
ALTER TABLE milestone_deferral       ADD CONSTRAINT milestone_deferral_milestone_id_fkey
    FOREIGN KEY (milestone_id) REFERENCES milestone_ref(id);
ALTER TABLE entity_milestone         ADD CONSTRAINT entity_milestone_milestone_id_fkey
    FOREIGN KEY (milestone_id) REFERENCES milestone_ref(id);

ALTER TABLE milestone_ref
    DROP COLUMN revision_id,
    DROP COLUMN valid_from,
    DROP COLUMN valid_to;
