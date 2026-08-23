-- App Registry — promotion_sync_event: observed ArgoCD sync/health log
-- (issue #1028, FR6, NFR4, NFR5; part of #978's Argo promotion-sync plan)
--
-- Mirrors migration 003's promotion_event shape and its own doc comment on
-- why sync/health transitions don't belong in promotion_event's fixed
-- `action` enum: sync/health observation is a DIFFERENT event stream than
-- the operator-driven promote/rollback/override/retire/approve/reject
-- actions promotion_event tracks -- one is "what did a human/system decide
-- to do", the other is "what did ArgoCD report back", and mixing them into
-- one CHECK-constrained `action` column would force every future ArgoCD
-- status string change to also touch the audit-log migration.
--
-- Schema-and-repository-only (this task): nothing writes real rows here
-- yet -- that's a later task once the poll/retry activities exist (see
-- issue #1028's own scope note). RecordSyncEvent/ListSyncEvents
-- (server/repository/repository.go) are fully implemented and tested
-- against both the fake and this table so that later task can call them
-- immediately.
CREATE TABLE promotion_sync_event (
    sync_event_id   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    promotion_id    UUID NOT NULL REFERENCES promotion (promotion_id),
    -- 'refresh_triggered' | 'poll_observed' | 'retry_triggered' | 'retry_observed'
    source          TEXT NOT NULL CHECK (source IN ('refresh_triggered', 'poll_observed', 'retry_triggered', 'retry_observed')),
    -- ArgoCD sync status string (e.g. Synced/OutOfSync/Unknown), '' if not applicable to this row (e.g. a *_triggered row)
    sync_status     TEXT NOT NULL DEFAULT '',
    -- ArgoCD health status string (e.g. Healthy/Progressing/Degraded/Suspended/Missing/Unknown), '' if not applicable
    health_status   TEXT NOT NULL DEFAULT '',
    occurred_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Two separate single-column indexes, NOT a composite one -- matching
-- promotion_event_promotion_id_idx / promotion_event_occurred_at_idx's pair
-- (migration 003), per FR6.
CREATE INDEX promotion_sync_event_promotion_id_idx ON promotion_sync_event (promotion_id);
CREATE INDEX promotion_sync_event_occurred_at_idx ON promotion_sync_event (occurred_at);
