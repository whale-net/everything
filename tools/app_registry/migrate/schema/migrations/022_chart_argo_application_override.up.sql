-- App Registry — chart ArgoCD Application name override
--
-- Adds `chart_argo_application_override`: an optional, admin-settable
-- per-(chart, environment) ArgoCD Application name, overriding what
-- WritebackWorkflow's TriggerArgoRefresh/PollArgoSyncStatus activities
-- target for that chart's promotions in that one environment
-- (worker/writeback/argosync.go), instead of the "<full_name>-<environment>"
-- convention (workflow.go/chartname.go). For ad-hoc/legacy deployments whose
-- real ArgoCD Application name doesn't follow that convention -- see
-- server/repository/models.go's Chart.ResolveArgoApplicationName and the
-- `chart set-argo-override` CLI command.
--
-- Keyed per (chart_id, environment_key) -- not a chart-wide value -- because
-- an ad-hoc deployment's naming can differ unrelatedly between environments,
-- e.g. dev named "foo-dev-app" and prod named "prod-svc-foo", sharing no
-- pattern. An environment absent from this table uses the convention;
-- environments are independent of each other.
--
-- Deliberately a SEPARATE TABLE, not a column on `chart`/`v_current_chart`:
-- postgres/app.go's chartColumns/v_current_chart back Reconcile's "mark
-- missing charts" sweep, which runs on every ReconcileApps call regardless
-- of which migration step a caller has applied. Two existing integration
-- tests pin the schema to an old migration step count (10, 14) and then
-- call the real ReconcileApps against that partial schema, to test those
-- specific migrations' up/down SQL in isolation
-- (postgres_integration_app_test.go's migration-010 test,
-- postgres_integration_artifact_test.go's migration-014 test) -- a column
-- added to chart/v_current_chart here would make Reconcile's sweep query
-- fail against any schema older than this migration, breaking both. A
-- separate table, joined only by the specific chart-fetching methods that
-- need it (GetChartByID/GetChartByFullName/ListCharts/ChartsForApp -- never
-- the reconcile sweep or the identity-only getChartByDomainName), avoids
-- that coupling entirely, now and for any future addition here.
CREATE TABLE chart_argo_application_override (
    chart_id               UUID NOT NULL REFERENCES chart(chart_id),
    environment_key        TEXT NOT NULL REFERENCES environment(key),
    argo_application_name  TEXT NOT NULL,
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (chart_id, environment_key)
);
