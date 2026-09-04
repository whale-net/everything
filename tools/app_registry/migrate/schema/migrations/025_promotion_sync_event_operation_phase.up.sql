-- App Registry — promotion_sync_event: add operation_phase column
--
-- ArgoCD's status.health rollup EXCLUDES hook resources (PreSync/Sync/
-- PostSync, e.g. a migration Job run via the argocd.argoproj.io/hook
-- annotation): an Application can report sync_status=Synced,
-- health_status=Healthy while a PostSync hook is still running. Only
-- status.operationState.phase (e.g. Running/Succeeded/Failed/Error) reflects
-- whether the most recent sync operation -- including every hook it ran --
-- has actually finished. Without this column, DerivePromotionSyncOutcome
-- (and the Promotion Details page's readiness banner) could classify a
-- promotion as ready for consumption before a hook like a DB migration had
-- even been scheduled.
ALTER TABLE promotion_sync_event
    ADD COLUMN operation_phase TEXT NOT NULL DEFAULT '';
