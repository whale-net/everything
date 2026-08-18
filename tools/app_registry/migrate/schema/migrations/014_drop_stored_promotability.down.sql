-- Restore artifact.promotability as a STORED column, backfilled from the
-- CURRENT v_current_app/v_current_chart deploy_unit -- mirrors migration
-- 008's original backfill, adjusted for the views migration 010 introduced
-- (008 joined the app/chart base tables directly; those columns no longer
-- exist on app/chart post-AR-7c, so this backfill has to go through the
-- views instead). Matches repository.DerivePromotability (server/
-- repository/promotability.go) exactly, including binary/firmware's
-- kind-only branches (#810).

ALTER TABLE artifact ADD COLUMN promotability TEXT
    CHECK (promotability IN ('promotable', 'via_chart', 'not_promotable'));

UPDATE artifact
SET promotability = 'not_promotable'
WHERE kind = 'firmware' AND state = 'published';

UPDATE artifact
SET promotability = 'promotable'
WHERE kind = 'binary' AND state = 'published';

UPDATE artifact a
SET promotability = CASE app.deploy_unit
    WHEN 'chart' THEN 'via_chart'
    WHEN 'image' THEN 'promotable'
    ELSE 'not_promotable'
END
FROM v_current_app app
WHERE a.kind = 'image' AND a.app_id = app.app_id AND a.state = 'published';

UPDATE artifact
SET promotability = 'promotable'
WHERE kind = 'chart' AND state = 'published';

ALTER TABLE artifact ADD CONSTRAINT artifact_promotability_shape CHECK (
    (state = 'published' AND promotability IS NOT NULL) OR
    (state != 'published' AND promotability IS NULL)
);
