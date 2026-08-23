-- App Registry — chart ArgoCD Application name override
--
-- Adds `argo_application_name_template` to `chart`: an optional,
-- admin-settable override for the ArgoCD Application name
-- WritebackWorkflow's TriggerArgoRefresh/PollArgoSyncStatus activities
-- target for this chart's promotions (worker/writeback/argosync.go),
-- instead of the "<full_name>-<environment>" convention
-- (workflow.go/chartname.go). For ad-hoc/legacy deployments whose real
-- ArgoCD Application name doesn't follow that convention -- see
-- server/repository/models.go's Chart.ResolveArgoApplicationName and the
-- `chart set-argo-override` CLI command.
--
-- Default '' means no override (every standard chart). ReconcileApps
-- (server/repository/postgres/app.go) never writes this column -- only
-- SetChartArgoApplicationNameOverride does -- so an admin-set override
-- survives reconciliation. This is a distinct, new mechanism from
-- Environment.gitops_path (migration 002), which is dead code -- see
-- architecture/12-writeback-outbox-temporal.md.
ALTER TABLE chart ADD COLUMN argo_application_name_template TEXT NOT NULL DEFAULT '';

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
    c.argo_application_name_template
FROM chart c;
