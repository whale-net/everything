-- outcome_bar: the per-Channel outcome bar's storage half (C14 / FR1 /
-- FR2 / NFR1, issue #1882). Natural key = Channel (channel_id UNIQUE) so
-- OutcomeBarStore.Upsert converges on repeated calls with identical
-- values, the same idempotent-by-construction shape pacing_policy used
-- before migration 013 dropped it (see
-- .../002_research_schedule_outcome.up.sql lines 83-90, the DDL this
-- table's column layout is copied from).
--
-- Deliberately NOT SCD2 (AGENTS.md § SCD2 does not apply): FR4 always
-- classifies against the Channel's *currently* configured bar, and
-- historical/versioned thresholds are explicitly out of scope for M3.
-- This is a current-state config row, exactly as pacing_policy was --
-- hence no valid_from/valid_to here.
--
-- Deliberately NO `CHECK (metric_name IN ('views'))`. FR1's whole point
-- is that a later milestone can add a bar metric with no schema
-- migration; the views-only restriction is enforced in Go
-- (store.OutcomeBarMetricViews / ErrUnsupportedOutcomeBarMetric,
-- store/outcome_bar.go) precisely so relaxing it is a code change, not a
-- migration.

CREATE TABLE outcome_bar (
    id                    UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id            UUID        NOT NULL UNIQUE REFERENCES channel(id),
    metric_name           TEXT        NOT NULL,
    threshold_value       NUMERIC     NOT NULL,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_by_person_id  UUID        NOT NULL REFERENCES person(id)
);
