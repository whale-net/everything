-- Rollback App Registry App Identity / Manifest Snapshot Split (AR-7c, issue #558)
--
-- Matches the style of 007's down: restores the pre-migration column shape
-- and backfills it from the best available data, but does NOT attempt to
-- preserve full snapshot history -- the same tradeoff 003's down (DROP TABLE
-- promotion) and 007's down (DELETE FROM artifact WHERE state != 'published')
-- already make for this repo's migrations. Once this runs, app_manifest/
-- chart_manifest and everything in them are gone; only the single "latest
-- known" value per app/chart survives, folded back into the old flat
-- columns.

-- ============================================================================
-- Drop the views first -- they reference columns this migration is about to
-- resurrect on app/chart and drop from artifact, so they must go before
-- either ALTER.
-- ============================================================================

DROP VIEW IF EXISTS v_current_chart;
DROP VIEW IF EXISTS v_current_app;

-- ============================================================================
-- Restore app / chart's mutable columns, backfilled from the newest
-- snapshot per owner (any provenance -- unlike v_current_app, this rollback
-- is a one-time best-effort data recovery, not a live read path, so there is
-- no reason to prefer 'sweep' over 'release' here).
-- ============================================================================

ALTER TABLE app
    ADD COLUMN description       TEXT NOT NULL DEFAULT '',
    ADD COLUMN language           TEXT NOT NULL DEFAULT '',
    ADD COLUMN app_type           TEXT NOT NULL DEFAULT '',
    ADD COLUMN deploy_unit        TEXT NOT NULL DEFAULT 'chart' CHECK (deploy_unit IN ('chart', 'image', 'none')),
    ADD COLUMN bazel_label        TEXT NOT NULL DEFAULT '',
    ADD COLUMN image_repository   TEXT NOT NULL DEFAULT '';

UPDATE app a
SET
    description      = COALESCE(m.manifest_json ->> 'description', ''),
    language          = COALESCE(m.manifest_json ->> 'language', ''),
    app_type          = COALESCE(m.manifest_json ->> 'app_type', ''),
    deploy_unit       = COALESCE(m.deploy_unit, 'chart'),
    bazel_label       = COALESCE(m.manifest_json ->> 'binary_target', ''),
    image_repository  = COALESCE(m.image_repository, '')
FROM LATERAL (
    SELECT deploy_unit, image_repository, manifest_json
    FROM app_manifest
    WHERE owner_id = a.app_id
    ORDER BY source_committed_at DESC, recorded_at DESC
    LIMIT 1
) m;

ALTER TABLE chart
    ADD COLUMN description       TEXT NOT NULL DEFAULT '',
    ADD COLUMN chart_repository  TEXT NOT NULL DEFAULT '',
    ADD COLUMN deploy_unit        TEXT NOT NULL DEFAULT 'chart' CHECK (deploy_unit IN ('chart', 'image', 'none'));

-- chart.deploy_unit was always the hardcoded 'chart' constant (see the up
-- migration's "Why chart_manifest has no generated columns") -- the column
-- default above already restores that for every row, nothing to backfill
-- from chart_manifest.

-- ============================================================================
-- Drop artifact.manifest_id / promotability
-- ============================================================================

ALTER TABLE artifact DROP CONSTRAINT IF EXISTS artifact_promotability_shape;
ALTER TABLE artifact DROP COLUMN IF EXISTS promotability;
ALTER TABLE artifact DROP COLUMN IF EXISTS manifest_id;

-- ============================================================================
-- Drop the snapshot tables
-- ============================================================================

DROP TABLE IF EXISTS chart_manifest;
DROP TABLE IF EXISTS app_manifest;
