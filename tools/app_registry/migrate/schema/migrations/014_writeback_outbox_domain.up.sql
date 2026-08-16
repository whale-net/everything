-- App Registry — domain-scoped writeback (issue #798)
--
-- Adds writeback_outbox.domain, denormalized at write time exactly like
-- environment_key on the same table (see 004_writeback_outbox.up.sql's
-- doc comment on that column for the rationale this copies): the worker's
-- Publish activity needs to know which argok8s directory
-- (<domain>/versions/<env>.yaml, see
-- tools/app_registry/architecture/12-writeback-outbox-temporal.md) a row's
-- rendered document belongs to without a join back through
-- promotion -> artifact -> app/chart on every drain pass.
--
-- Added nullable first, then backfilled, then NOT NULL enforced -- matching
-- migration 007's ADD-COLUMN-then-backfill style (see that file's comment
-- on migration 005's precedent) so the NOT NULL constraint below applies
-- cleanly to any pre-existing rows.
ALTER TABLE writeback_outbox ADD COLUMN domain TEXT;

-- Backfill: domain is owned by whichever app or chart the row's promotion's
-- artifact belongs to (repository.PromotionServer.ownerDomain's server-side
-- logic, applied here as one UPDATE instead of a per-row RPC loop).
UPDATE writeback_outbox wo
SET domain = COALESCE(app.domain, chart.domain)
FROM promotion p
JOIN artifact a ON a.artifact_id = p.artifact_id
LEFT JOIN app ON app.app_id = a.app_id
LEFT JOIN chart ON chart.chart_id = a.chart_id
WHERE p.promotion_id = wo.promotion_id;

ALTER TABLE writeback_outbox ALTER COLUMN domain SET NOT NULL;
