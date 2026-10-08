-- App Registry — promotion_sync_event: add sync_revision / operation_revision
--
-- Right after a writeback, ArgoCD (or the ApplicationSet that generates the
-- Application) has not yet picked up the new desired state and keeps
-- reporting the PREVIOUS release as Synced/Healthy/Succeeded. Recording the
-- revisions ArgoCD reported each status against lets the worker and
-- DerivePromotionSyncOutcome only call a promotion healthy once ArgoCD is on
-- the promotion's own revision. Comma-joined; '' on trigger rows and on rows
-- recorded before this migration.
ALTER TABLE promotion_sync_event
    ADD COLUMN sync_revision TEXT NOT NULL DEFAULT '',
    ADD COLUMN operation_revision TEXT NOT NULL DEFAULT '';
