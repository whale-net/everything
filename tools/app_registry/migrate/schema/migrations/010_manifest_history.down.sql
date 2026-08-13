-- Rollback SCD2 manifest history (AR-8, issue #587)
--
-- Follows migration 008's own down.sql precedent for this migration set
-- (also 003's and 007's): best-effort data recovery, not full-history
-- preservation. Once this runs, app_manifest_history/chart_manifest_history/
-- app_manifest_release/chart_manifest_release/reconcile_run and everything
-- content-addressing bought are gone; app_manifest/chart_manifest go back to
-- one row per (owner, git_sha) -- reconstructed from the SCD2 intervals
-- (one row per interval, attributed to first_git_sha) plus the release
-- table (one row per release, as-is) -- which is strictly LESS granular than
-- what migration 008's shape actually had (every no-op sweep's row is gone
-- for good), but that granularity is the entire thing this migration exists
-- to stop tracking, so there is nothing better to reconstruct it from.

-- ============================================================================
-- Drop the view first -- it's about to be rebuilt against the restored shape
-- ============================================================================

DROP VIEW v_current_app;

-- ============================================================================
-- Move the content-addressed tables out of the way, then recreate
-- app_manifest/chart_manifest in migration 008's shape
-- ============================================================================

ALTER TABLE app_manifest RENAME TO app_manifest_new;
ALTER TABLE chart_manifest RENAME TO chart_manifest_new;

CREATE TABLE app_manifest (
    app_manifest_id       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id              UUID NOT NULL REFERENCES app (app_id),
    source_git_sha        TEXT NOT NULL,
    source_committed_at   BIGINT NOT NULL DEFAULT 0,
    provenance            TEXT NOT NULL CHECK (provenance IN ('sweep', 'release')),
    manifest_json          JSONB NOT NULL,
    deploy_unit            TEXT GENERATED ALWAYS AS (
        CASE manifest_json ->> 'deploy_unit'
            WHEN 'DEPLOY_UNIT_IMAGE' THEN 'image'
            WHEN 'DEPLOY_UNIT_NONE'  THEN 'none'
            ELSE 'chart'
        END
    ) STORED,
    image_repository       TEXT GENERATED ALWAYS AS (
        CASE
            WHEN COALESCE(manifest_json ->> 'registry', '') = ''
             AND COALESCE(manifest_json ->> 'organization', '') = ''
             AND COALESCE(manifest_json ->> 'repo_name', '') = ''
            THEN ''
            ELSE COALESCE(manifest_json ->> 'registry', '') || '/' ||
                 COALESCE(manifest_json ->> 'organization', '') || '/' ||
                 COALESCE(manifest_json ->> 'repo_name', '')
        END
    ) STORED,
    recorded_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (owner_id, source_git_sha)
);

CREATE INDEX app_manifest_current_idx
    ON app_manifest (owner_id, provenance, source_committed_at DESC, recorded_at DESC);

CREATE TABLE chart_manifest (
    chart_manifest_id      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id                UUID NOT NULL REFERENCES chart (chart_id),
    source_git_sha          TEXT NOT NULL,
    source_committed_at     BIGINT NOT NULL DEFAULT 0,
    provenance               TEXT NOT NULL CHECK (provenance IN ('sweep', 'release')),
    manifest_json             JSONB NOT NULL,
    recorded_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (owner_id, source_git_sha)
);

CREATE INDEX chart_manifest_current_idx
    ON chart_manifest (owner_id, provenance, source_committed_at DESC, recorded_at DESC);

-- ============================================================================
-- Backfill from app_manifest_history: one row per interval, attributed to
-- first_git_sha (the commit the content first appeared at). source_committed_at
-- is reconstructed from valid_from (EXTRACT(EPOCH ...)) -- an approximation
-- for any interval whose valid_from was itself a recorded_at fallback (the
-- source_committed_at = 0 sentinel case, see the up migration) -- there is no
-- way to distinguish that case here, which is exactly the kind of precision
-- this rollback does not promise. recorded_at is likewise reconstructed from
-- valid_from -- the original per-write wall-clock timestamp isn't preserved
-- across an interval.
-- ============================================================================

INSERT INTO app_manifest (owner_id, source_git_sha, source_committed_at, provenance, manifest_json, recorded_at)
SELECT
    h.owner_id,
    h.first_git_sha,
    EXTRACT(EPOCH FROM h.valid_from)::BIGINT,
    'sweep',
    m.manifest_json,
    h.valid_from
FROM app_manifest_history h
JOIN app_manifest_new m ON m.app_manifest_id = h.app_manifest_id;

INSERT INTO chart_manifest (owner_id, source_git_sha, source_committed_at, provenance, manifest_json, recorded_at)
SELECT
    h.owner_id,
    h.first_git_sha,
    EXTRACT(EPOCH FROM h.valid_from)::BIGINT,
    'sweep',
    m.manifest_json,
    h.valid_from
FROM chart_manifest_history h
JOIN chart_manifest_new m ON m.chart_manifest_id = h.chart_manifest_id;

-- ============================================================================
-- Backfill from *_release: recorded_at IS preserved exactly (the release
-- table always kept it); source_committed_at was never stored on that table
-- either, so it is 0 here too, same as it would be for a caller running
-- against a pre-AR-7c release_helper_go binary (see migration 006's doc
-- comment on that fallback).
-- ============================================================================

INSERT INTO app_manifest (owner_id, source_git_sha, source_committed_at, provenance, manifest_json, recorded_at)
SELECT r.owner_id, r.git_sha, 0, 'release', m.manifest_json, r.recorded_at
FROM app_manifest_release r
JOIN app_manifest_new m ON m.app_manifest_id = r.app_manifest_id;

INSERT INTO chart_manifest (owner_id, source_git_sha, source_committed_at, provenance, manifest_json, recorded_at)
SELECT r.owner_id, r.git_sha, 0, 'release', m.manifest_json, r.recorded_at
FROM chart_manifest_release r
JOIN chart_manifest_new m ON m.chart_manifest_id = r.chart_manifest_id;

-- ============================================================================
-- Remap artifact.manifest_id: new content id -> one representative restored
-- row sharing the same (owner, manifest_json). DISTINCT ON picks the
-- earliest recorded_at deterministically when more than one restored row
-- shares the content (a sweep interval and a release both observing the
-- same manifest, for instance).
-- ============================================================================

UPDATE artifact a
SET manifest_id = restored.app_manifest_id
FROM (
    SELECT DISTINCT ON (m.app_manifest_id) m.app_manifest_id AS new_id, am.app_manifest_id
    FROM app_manifest_new m
    JOIN app_manifest am ON am.owner_id = m.owner_id AND am.manifest_json = m.manifest_json
    ORDER BY m.app_manifest_id, am.recorded_at
) restored
WHERE a.kind = 'image' AND a.manifest_id = restored.new_id;

UPDATE artifact a
SET manifest_id = restored.chart_manifest_id
FROM (
    SELECT DISTINCT ON (m.chart_manifest_id) m.chart_manifest_id AS new_id, cm.chart_manifest_id
    FROM chart_manifest_new m
    JOIN chart_manifest cm ON cm.owner_id = m.owner_id AND cm.manifest_json = m.manifest_json
    ORDER BY m.chart_manifest_id, cm.recorded_at
) restored
WHERE a.kind = 'chart' AND a.manifest_id = restored.new_id;

-- ============================================================================
-- Drop everything AR-8 added
-- ============================================================================

DROP TABLE reconcile_run;
DROP TABLE app_manifest_release;
DROP TABLE chart_manifest_release;
DROP TABLE app_manifest_history;
DROP TABLE chart_manifest_history;
DROP TABLE app_manifest_new;
DROP TABLE chart_manifest_new;

-- ============================================================================
-- Restore v_current_app to migration 008's exact form
-- ============================================================================

CREATE VIEW v_current_app AS
SELECT
    a.app_id,
    a.domain,
    a.name,
    COALESCE(m.manifest_json ->> 'description', '')   AS description,
    COALESCE(m.manifest_json ->> 'language', '')       AS language,
    COALESCE(m.manifest_json ->> 'app_type', '')       AS app_type,
    COALESCE(m.deploy_unit, 'chart')                   AS deploy_unit,
    COALESCE(m.manifest_json ->> 'binary_target', '')  AS bazel_label,
    COALESCE(m.image_repository, '')                   AS image_repository,
    a.status,
    a.first_seen_at,
    a.last_seen_at
FROM app a
LEFT JOIN LATERAL (
    SELECT deploy_unit, image_repository, manifest_json
    FROM app_manifest
    WHERE owner_id = a.app_id AND provenance = 'sweep'
    ORDER BY source_committed_at DESC, recorded_at DESC
    LIMIT 1
) m ON true;
