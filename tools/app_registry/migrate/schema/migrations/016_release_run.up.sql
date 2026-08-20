-- App Registry — release_run audit trail (NFR4, issue #887)
--
-- Adds the UI-trigger-layer audit story's second half. `build`/`artifact`
-- (migration 007) + GetReleaseRun remain the record of what was actually
-- built/published, unchanged — see
-- architecture/08-release-lifecycle/04-run-log.md "The run log: CI
-- orchestrates, the registry records". This migration adds the half only
-- the UI-trigger layer knows before any of that happens: who triggered a
-- release, what scope they asked for, whether it was a digest-or-fresh
-- input, the single resolved plan, and per-target step state as the
-- release progresses.
--
-- Not SCD2 — see AGENTS.md "SCD2": these are append-only/state-machine
-- rows, not a current-value dimension. `release_run` is written once and
-- never mutated after create (temporal_run_id may be filled in once the
-- workflow starts, but there is no valid_from/valid_to open-close dance).
-- `release_run_target` follows `artifact`'s existing mutation style
-- (migration 007) — a row IS the current state of one target within a
-- batch, transitioned in place via UPDATE, not superseded by a new row per
-- transition.

-- release_run: one row per triggered release (a batch). One release may
-- cover many targets (apps/charts) — see release_run_target below.
CREATE TABLE release_run (
    release_run_id       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- triggered_by: the authenticated user who triggered this release (FR1).
    triggered_by          TEXT NOT NULL,
    -- requested_scope: the raw `all`/domain/comma-list input as given by the
    -- trigger (FR1) — free text, not normalized, so the audit trail always
    -- shows exactly what was asked for even if resolution narrowed it.
    requested_scope       TEXT NOT NULL,
    -- digest_input: FR2's per-target digest map, when the trigger pinned
    -- specific digests instead of asking for a fresh build. NULL means
    -- "build fresh" — this is presence/absence information, not an empty
    -- map standing in for "no input given".
    digest_input          JSONB,
    -- resolved_plan: the single plan resolved for this release (FR7/FR8),
    -- written once at CreateReleaseRun time and never rewritten — so no
    -- downstream consumer (a resumed/retried step, a status query, an
    -- audit read) ever needs to re-resolve it themselves.
    resolved_plan         JSONB NOT NULL,
    -- temporal_workflow_id: the workflow-id-based dedup key (FR5/NFR2) —
    -- the actual "only one non-terminal release per target" guarantee is
    -- Temporal's deterministic workflow-id dedup, not a DB constraint (see
    -- release_run_target_owner_state_idx's comment below). UNIQUE here so a
    -- retried CreateReleaseRun call for the same workflow id is at least
    -- caught at the audit-row layer too.
    temporal_workflow_id  TEXT NOT NULL,
    -- temporal_run_id: filled in once the workflow actually starts running
    -- under temporal_workflow_id. Empty (not NULL) until then, matching
    -- writeback_outbox's NOT NULL DEFAULT '' convention for "not yet known"
    -- text fields (004_writeback_outbox.up.sql).
    temporal_run_id       TEXT NOT NULL DEFAULT '',
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX release_run_temporal_workflow_id_idx ON release_run (temporal_workflow_id);

-- release_run_target: one row per target (app or chart) in a release_run's
-- batch, append-only in the sense that CreateReleaseRun inserts exactly one
-- row per target up front (all starting 'queued') and the release only
-- ever advances an existing row's state in place — never inserts a second
-- row for the same target within the same release_run.
CREATE TABLE release_run_target (
    release_run_target_id  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    release_run_id         UUID NOT NULL REFERENCES release_run (release_run_id) ON DELETE CASCADE,
    owner_full_name         TEXT NOT NULL,
    -- kind: matches ArtifactKind naming (server/repository/models.go) —
    -- 'image'/'chart' (this table only ever targets a build/publish step,
    -- never the binary/firmware artifact kinds migration 011 added to
    -- `artifact`).
    kind                    TEXT NOT NULL CHECK (kind IN ('image', 'chart')),
    state                   TEXT NOT NULL DEFAULT 'queued' CHECK (
        state IN ('queued', 'building', 'publishing', 'recording', 'succeeded', 'failed')
    ),
    state_changed_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- build_id: linked once known — a target's build isn't chosen until
    -- the 'building' step actually runs one, so this starts NULL. No FK
    -- CASCADE behavior needed beyond the default (a `build` row is never
    -- deleted — see migration 001's comment on `build`).
    build_id                UUID REFERENCES build (build_id),
    error_detail            TEXT NOT NULL DEFAULT ''
);

CREATE INDEX release_run_target_release_run_id_idx ON release_run_target (release_run_id);

-- Supports "is there a non-terminal release for this target" lookups
-- (FR5/NFR2) for the UI/status query path. NOTE: the actual concurrency
-- guarantee against two concurrent releases racing the same target comes
-- from Temporal's workflow-id-based dedup (release_run.temporal_workflow_id
-- above), owned by the release-trigger workflow, not from this index or any
-- DB constraint on this table — this index only makes the status query
-- cheap, it is not load-bearing for correctness.
CREATE INDEX release_run_target_owner_state_idx ON release_run_target (owner_full_name, state);
