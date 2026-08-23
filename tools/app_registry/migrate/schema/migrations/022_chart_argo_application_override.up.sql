-- App Registry — chart ArgoCD Application name override
--
-- Adds `argo_application_name_overrides` to `chart`: an optional,
-- admin-settable map of environment_key -> ArgoCD Application name,
-- overriding what WritebackWorkflow's TriggerArgoRefresh/PollArgoSyncStatus
-- activities target for this chart's promotions in that one environment
-- (worker/writeback/argosync.go), instead of the "<full_name>-<environment>"
-- convention (workflow.go/chartname.go). For ad-hoc/legacy deployments whose
-- real ArgoCD Application name doesn't follow that convention -- see
-- server/repository/models.go's Chart.ResolveArgoApplicationName and the
-- `chart set-argo-override` CLI command.
--
-- Keyed per environment (not a single per-chart value) because an ad-hoc
-- deployment's naming can differ unrelatedly between environments -- e.g.
-- dev named "foo-dev-app" and prod named "prod-svc-foo", sharing no
-- pattern. An environment absent from the map uses the convention;
-- environments are independent of each other.
--
-- Default '{}' means no overrides (every standard chart). ReconcileApps
-- (server/repository/postgres/app.go) never writes this column -- only
-- SetChartArgoApplicationNameOverride does -- so admin-set overrides
-- survive reconciliation. This is a distinct, new mechanism from
-- Environment.gitops_path (migration 002), which is dead code -- see
-- architecture/12-writeback-outbox-temporal.md.
ALTER TABLE chart ADD COLUMN argo_application_name_overrides JSONB NOT NULL DEFAULT '{}'::jsonb;

-- v_current_chart (migration 008) is identity ⋈ constants -- append the new
-- identity column at the end so chartColumns/scanChart in postgres/app.go
-- can read it via GetChartByID/GetChartByFullName/ListCharts. CREATE OR
-- REPLACE VIEW requires the existing column list/order to stay exactly as
-- migration 008 defined it; only appending is allowed.
CREATE OR REPLACE VIEW v_current_chart AS
SELECT
    c.chart_id,
    c.domain,
    c.name,
    ''::text     AS description,
    ''::text     AS chart_repository,
    'chart'::text AS deploy_unit,
    c.status,
    c.first_seen_at,
    c.last_seen_at,
    c.argo_application_name_overrides
FROM chart c;
