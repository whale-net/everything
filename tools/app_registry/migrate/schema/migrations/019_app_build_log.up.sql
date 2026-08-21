-- App Registry — app_build_log: unconditional per-app/chart build log
-- (issue #923, FR8-FR12/FR14; part of #912's release-v2 pivot)
--
-- Answers "what commit is owner X's latest logged build" so v2's
-- release-trigger-time GitHubDispatcher.Dispatch (worker/release/github.go)
-- can eventually pin its `ref` to a commit ci.yml's reconcile job has
-- actually processed for that target, instead of dispatching against the
-- literal `main` branch pointer (which can race ahead of what was actually
-- reconciled -- the same class of ordering bug #547 closed for v1's
-- RecordArtifact path; FR14 documents the v2-path analogue in
-- architecture/08-release-lifecycle/00-overview.md's ordering table).
--
-- Deliberately UNCONDITIONAL, unlike migration 010's app_manifest_history:
-- ci.yml's reconcile-app-registry job is meant to write one row per
-- discovered app/chart on EVERY push, regardless of whether content
-- changed -- there is no "same content, skip the write" branch here (see
-- repository.AppBuildLogRepository's doc comment; do not accidentally
-- reuse recordAppManifestSweep's content-gating logic against this table).
-- Row count therefore scales with CI push frequency, not with content
-- churn -- the opposite of migration 010's whole point -- but this table
-- only ever needs its CURRENT row per owner (the partial index below), so
-- that tradeoff is cheap: history accumulates but is read only for
-- point-in-time debugging, never on the hot path.
--
-- Deliberately SLIM (see AGENTS.md "SCD2" and
-- repository.AppBuildLogRepository's doc comment): no `digest`,
-- `repository`, or other image-related columns. This table only answers
-- "what commit", nothing about what was built from it -- no image build,
-- no digest, no registry push happens as part of writing this row.
--
-- owner_id is intentionally NOT foreign-keyed to a single table, mirroring
-- `artifact.owner_id`'s convention (migration 001): `kind` disambiguates
-- whether owner_id is an `app.app_id` or a `chart.chart_id`. This keeps
-- FR9's release-target resolution (release_run_target.kind is already
-- IMAGE/CHART, migration 016) to ONE table instead of an
-- app_build_log/chart_build_log split like migration 010's
-- app_manifest_history/chart_manifest_history -- "app_build_log" is kept as
-- the table name for continuity with this issue's own title, even though
-- it is not app-only.
CREATE TABLE app_build_log (
    app_build_log_id  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id          UUID NOT NULL,
    kind              TEXT NOT NULL CHECK (kind IN ('image', 'chart')),
    git_sha           TEXT NOT NULL,
    -- build_id: obtained via the existing RecordBuild RPC -- ONE call per
    -- push/workflow-run, not per app/chart (see migration 001's `build`
    -- table doc comment: "one CI run"). Every app_build_log row written for
    -- the same push shares the same build_id.
    build_id          UUID NOT NULL REFERENCES build (build_id),
    valid_from        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to          TIMESTAMPTZ
);

-- Partial unique index for O(1) current-pointer lookup (FR9/FR10) -- same
-- shape as migration 010's app_manifest_history_current_idx.
CREATE UNIQUE INDEX app_build_log_current_idx
    ON app_build_log (owner_id) WHERE valid_to IS NULL;
