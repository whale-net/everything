-- App Registry — Promotion (AR-3c)
--
-- Adds the `promotion`/`promotion_event` tables described in
-- ARCHITECTURE.md's "Data model" and "SCD2 on promotion". `promotion` is
-- SCD2 history following the repo-wide valid_from/valid_to convention in
-- AGENTS.md; `promotion_event` is a separate append-only audit log — per
-- AGENTS.md, SCD2 does not apply to event logs.
--
-- Deliberately kept out of 001/002 (see migrate/README.md "Planned
-- migrations"): this needs environment.environment_id (002) and
-- artifact.artifact_id (001) as FK targets.

-- promotion: what is deployed where, right now and historically. Rows are
-- never updated in place except to close them (valid_to); a new promotion
-- is always a new row. `target_key` is "<kind>:<owner_full_name>" — the
-- promoted thing's identity, denormalized so the partial unique index below
-- is expressible without a nullable two-column target (app_id is NULL for
-- chart artifacts and vice versa, and a unique index cannot treat two NULLs
-- as equal). Everything else about the promoted artifact (repository,
-- version, digest, kind, owner) is read via the artifact join rather than
-- copied here — see promotionSelectBase in
-- server/repository/postgres/promotion.go — so there is exactly one place
-- that data can drift from.
CREATE TABLE promotion (
    promotion_id    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    environment_id  UUID NOT NULL REFERENCES environment (environment_id),
    target_key      TEXT NOT NULL,
    artifact_id     UUID NOT NULL REFERENCES artifact (artifact_id),
    -- PromotionState in messages.proto. pending_approval/failed are reserved
    -- for the approval gate described in ARCHITECTURE.md "Future: approval
    -- gate" — AR-3c only ever writes 'active', and a row that has been
    -- closed (valid_to set) is implicitly "superseded" without needing its
    -- own state value change (the SCD2 window already encodes that).
    state           TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('pending_approval', 'active', 'superseded', 'failed')),
    -- True when an image belonging to a DEPLOY_UNIT_CHART app was promoted
    -- directly, bypassing its chart -- see ARCHITECTURE.md "Promotability".
    is_override     BOOLEAN NOT NULL DEFAULT false,
    valid_from      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to        TIMESTAMPTZ
);

-- Load-bearing, not an optimization (see migrate/README.md "Required
-- indexes"): makes double-promotion of the same target in the same
-- environment structurally impossible, and backs the hot "current state"
-- read.
CREATE UNIQUE INDEX promotion_current_idx
    ON promotion (environment_id, target_key)
    WHERE valid_to IS NULL;

-- Backs GetEnvironmentState's "state at time T" query and
-- ListPromotions(include_history = true): walking a target's history newest
-- first.
CREATE INDEX promotion_window_idx
    ON promotion (environment_id, target_key, valid_from DESC);

CREATE INDEX promotion_artifact_id_idx ON promotion (artifact_id);

-- promotion_event: append-only audit log. One Promote/Rollback call writes
-- exactly one event; the approval gate (unbuilt) would add APPROVE/REJECT
-- events against the same promotion_id. NOT SCD2 — see AGENTS.md, event
-- logs get their own shape, not valid_from/valid_to.
CREATE TABLE promotion_event (
    event_id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    promotion_id          UUID NOT NULL REFERENCES promotion (promotion_id),
    action                TEXT NOT NULL CHECK (action IN ('promote', 'rollback', 'override', 'retire', 'approve', 'reject')),
    actor                 TEXT NOT NULL DEFAULT '',
    -- Free text. Required by the handler (not this schema) when the target
    -- environment's rank > 0 -- see ARCHITECTURE.md "Authorization".
    reason                TEXT NOT NULL DEFAULT '',
    -- Set once the writeback worker (AR-4) picks this up, so an operator can
    -- jump from an audit row to the workflow history. Empty string until then.
    temporal_workflow_id  TEXT NOT NULL DEFAULT '',
    temporal_run_id       TEXT NOT NULL DEFAULT '',
    occurred_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX promotion_event_promotion_id_idx ON promotion_event (promotion_id);
CREATE INDEX promotion_event_occurred_at_idx ON promotion_event (occurred_at);

-- v_current_promotion: pre-joins promotion (valid_to IS NULL) -> artifact ->
-- environment so the CLI, the writeback worker, and any future UI never
-- re-derive this join -- see AGENTS.md "Views". Column order matches
-- server/repository/postgres/promotion.go's scanPromotion so the same Go
-- code can scan either this view or the equivalent base-table query used
-- for historical (non-current) reads.
CREATE VIEW v_current_promotion AS
SELECT
    p.promotion_id,
    p.environment_id,
    e.key AS environment_key,
    a.kind,
    a.app_id,
    a.chart_id,
    p.artifact_id,
    a.repository,
    a.version,
    a.digest,
    p.state,
    p.is_override,
    p.valid_from,
    p.valid_to,
    p.target_key
FROM promotion p
JOIN environment e ON e.environment_id = p.environment_id
JOIN artifact a ON a.artifact_id = p.artifact_id
WHERE p.valid_to IS NULL;
