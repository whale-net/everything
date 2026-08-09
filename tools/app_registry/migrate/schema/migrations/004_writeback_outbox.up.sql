-- App Registry — Writeback outbox (AR-4b)
--
-- Adds `writeback_outbox`, the transactional outbox described in
-- ARCHITECTURE.md's "Writeback: outbox -> Temporal". Temporal cannot
-- enlist in a Postgres transaction, so the Promote/Rollback RPC writes one
-- row here inside the SAME transaction as the promotion + promotion_event
-- write (server/handlers/promotion.go's enqueueWriteback, extending AR-3c's
-- existing SCD2 close-and-open transaction rather than opening a second
-- one). If that transaction commits, the promotion and its writeback
-- intent both exist; if it aborts, neither does -- exactly the atomicity
-- property PLAN.md's AR-4b calls out as the core one to get right.
--
-- Append-only + claimed, not SCD2: this is a work queue, not a slowly
-- changing dimension -- see AGENTS.md, queue/event tables get their own
-- shape, not valid_from/valid_to.
CREATE TABLE writeback_outbox (
    outbox_id       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    promotion_id    UUID NOT NULL REFERENCES promotion (promotion_id),
    environment_id  UUID NOT NULL REFERENCES environment (environment_id),
    -- Denormalized so the worker can log which environment a row belongs
    -- to without a join, matching promotion.target_key's denormalization
    -- rationale in 003_promotion.up.sql.
    environment_key TEXT NOT NULL,
    -- The promotion_event this row corresponds to (one Promote/Rollback
    -- call writes exactly one of each). Lets the worker stamp
    -- temporal_workflow_id/temporal_run_id back onto promotion_event once
    -- the workflow starts -- see that column's doc comment in
    -- 003_promotion.up.sql ("Set once the writeback worker (AR-4) picks
    -- this up").
    event_id        UUID NOT NULL REFERENCES promotion_event (event_id),
    -- Content hash of the target environment's promoted state, computed by
    -- server/handlers/promotion.go's stateHash inside the SAME transaction
    -- that wrote the promotion -- so this value is guaranteed consistent
    -- with what a GetEnvironmentState call for environment_key would
    -- return immediately after commit. The Writeback activity's Publish
    -- step uses it to skip a no-op write -- see
    -- GetEnvironmentStateResponse.state_hash's doc comment.
    state_hash      TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'claimed', 'done', 'failed')),
    -- Set when a worker claims this row (see worker/outbox's ClaimBatch:
    -- `UPDATE ... WHERE outbox_id IN (SELECT ... FOR UPDATE SKIP LOCKED)`).
    -- A worker killed mid-run (AR-4b's exit criterion) leaves a row
    -- 'claimed' with a stale claimed_at; ClaimBatch reclaims it once
    -- claimed_at falls outside the worker's staleness window, rather than
    -- stranding it forever. Starting the same promotion's WritebackWorkflow
    -- twice is safe -- Temporal's own workflow-id dedup (id = promotion_id)
    -- plus this outbox's own idempotent Publish activity make redelivery
    -- harmless, per ARCHITECTURE.md.
    claimed_by      TEXT,
    claimed_at      TIMESTAMPTZ,
    -- Set once ExecuteWorkflow has (re-)started the WritebackWorkflow, or
    -- confirmed via Temporal's dedup that one is already running/has run
    -- under this workflow id. Always equal to promotion_id in practice
    -- (see ARCHITECTURE.md's workflow-id convention); stored anyway so a
    -- reader never has to assume it.
    workflow_id     TEXT NOT NULL DEFAULT '',
    completed_at    TIMESTAMPTZ,
    last_error      TEXT NOT NULL DEFAULT '',
    attempts        INT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Backs the drain query's pending half. Partial (only pending rows matter
-- for the hot path) -- mirrors promotion_current_idx's use of a partial
-- index for a status-scoped query in 003_promotion.up.sql.
CREATE INDEX writeback_outbox_pending_idx
    ON writeback_outbox (created_at)
    WHERE status = 'pending';

-- Backs the drain query's stale-reclaim half.
CREATE INDEX writeback_outbox_claimed_idx
    ON writeback_outbox (claimed_at)
    WHERE status = 'claimed';

-- Lets an operator (or a test) find every outbox row for a given
-- promotion, e.g. to confirm the atomic-write property end to end.
CREATE INDEX writeback_outbox_promotion_id_idx ON writeback_outbox (promotion_id);
