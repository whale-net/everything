-- App Registry — SCD2 manifest history (AR-8, issue #587)
--
-- See ARCHITECTURE.md "App identity vs. per-build manifest snapshot" -> "As
-- built (AR-8, migration 010)" for the full design. Summary: migration 008's
-- `app_manifest`/`chart_manifest` write one row per owner on EVERY `main`
-- commit that reconciles, unconditionally -- ~13.6k rows/year in this repo
-- today, ~99% byte-identical to their predecessor. Row count should scale
-- with CONTENT change, not with CI frequency. This migration splits content
-- from timeline:
--
--   1. `app_manifest`/`chart_manifest` become CONTENT-ADDRESSED: one row per
--      DISTINCT manifest per owner, ever, never UPDATEd after insert. Same
--      table names as migration 008, entirely different shape -- the old
--      per-commit-snapshot tables are renamed to `*_old` below, backfilled
--      from, and dropped at the end of this migration.
--   2. `app_manifest_history`/`chart_manifest_history` are the `main`
--      timeline -- SCD2 per AGENTS.md (`valid_from`/`valid_to`), written
--      ONLY by ReconcileApps (the sweep). `v_current_app` becomes a point
--      lookup (`WHERE valid_to IS NULL`) against a partial index, instead of
--      migration 008's `LEFT JOIN LATERAL ... ORDER BY ... LIMIT 1` scan.
--   3. `app_manifest_release`/`chart_manifest_release` are release-time
--      observations -- written ONLY by AssertApps, from any ref. Keeps
--      `resolveManifestForPublish`'s exact-git_sha preference working: a
--      build's promotability derives from ITS OWN manifest, not `main`'s
--      current one.
--   4. `reconcile_run` replaces the per-app-per-sweep record with one row
--      per sweep (~380/yr in this repo, not ~13.6k) -- strictly more
--      information than before for the 3% of rows that actually mattered.
--
-- `provenance` disappears as a column on the manifest tables -- it becomes
-- WHICH TABLE you are in, which is what it always meant (history = sweep,
-- release = release). `artifact.manifest_id` is untouched in shape (still a
-- polymorphic, non-FK UUID -- see migration 008's comment #4) but now points
-- at an IMMUTABLE content row instead of a per-commit snapshot row; many
-- artifacts built from identical content now share one manifest_id, which is
-- a side-effect of content-addressing, not a behavior change any reader
-- depends on.
--
-- Why not add valid_from/valid_to to the existing app_manifest in place:
-- app_manifest currently mixes two things that cannot share one interval
-- chain -- 'sweep' rows (main only, applied monotonically, guaranteed by the
-- reconcile_watermark, issue #545) and 'release' rows (AssertApps, from ANY
-- ref -- divergent branch, old tag, unmerged PR, no timeline at all). An old
-- tag re-released today would close main's open interval and open a stale
-- one if both shared one chain. The interval chain itself has to be
-- sweep-only, which needs a separate table, not a filtered column.
--
-- Why not content-dedupe alone (first_seen/last_seen on the content row, no
-- separate history table): A -> B -> A (edit a release_app attribute, then
-- revert it -- normal, not exotic) makes A's interval span B's with no
-- ordering to recover the real sequence from. A separate history table
-- represents A -> B -> A as three non-overlapping rows, which is the truth.

-- ============================================================================
-- 1. app_manifest / chart_manifest: content-addressed, immutable
-- ============================================================================

-- One row per DISTINCT manifest per owner, ever. Never UPDATEd after insert
-- -- see postgres/app.go's resolveOrCreate{App,Chart}ManifestContent: an
-- INSERT ... ON CONFLICT (owner_id, manifest_hash) DO NOTHING, falling back
-- to a SELECT by (owner_id, manifest_json) on conflict (JSONB equality, not
-- a hand-computed hash in Go -- Postgres is the only thing that needs to
-- agree with itself about the hash).
--
-- manifest_hash's GENERATED expression requires md5(manifest_json::text) to
-- be IMMUTABLE: JSONB's canonical text output (jsonb_out) does not depend on
-- session settings the way e.g. timestamptz::text does, and md5(text) is
-- itself IMMUTABLE -- both are catalogued IMMUTABLE in Postgres, so this
-- holds for any Postgres version this repo targets (16, per libs/go/dbtest's
-- DefaultImage). Verify against the real `Test Database Integration` CI job
-- before relying on this further -- this repo's dev sandbox has no Docker
-- available to confirm locally (see PLAN.md/this PR's description).
CREATE TABLE app_manifest (
    app_manifest_id     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id            UUID NOT NULL REFERENCES app (app_id),
    manifest_json        JSONB NOT NULL,
    manifest_hash         TEXT GENERATED ALWAYS AS (md5(manifest_json::text)) STORED,

    -- Same two generated columns and same CASE expressions as migration
    -- 008's app_manifest -- unchanged behavior, just relocated to the
    -- content-addressed table. See that migration's comments for why only
    -- these two, and postgres/app.go's deployUnitToDB/imageRepository.
    deploy_unit           TEXT GENERATED ALWAYS AS (
        CASE manifest_json ->> 'deploy_unit'
            WHEN 'DEPLOY_UNIT_IMAGE' THEN 'image'
            WHEN 'DEPLOY_UNIT_NONE'  THEN 'none'
            ELSE 'chart'
        END
    ) STORED,
    image_repository      TEXT GENERATED ALWAYS AS (
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

    first_recorded_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (owner_id, manifest_hash)
);

-- chart_manifest: no generated columns, same reason as migration 008 --
-- ChartManifest carries no deploy_unit/image-repository concept at all.
CREATE TABLE chart_manifest (
    chart_manifest_id    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id              UUID NOT NULL REFERENCES chart (chart_id),
    manifest_json          JSONB NOT NULL,
    manifest_hash           TEXT GENERATED ALWAYS AS (md5(manifest_json::text)) STORED,
    first_recorded_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (owner_id, manifest_hash)
);

-- ============================================================================
-- 2. app_manifest_history / chart_manifest_history: the `main` timeline
-- ============================================================================

-- SCD2 per AGENTS.md. Written ONLY by ReconcileApps
-- (postgres/app.go's recordAppManifestSweep/recordChartManifestSweep),
-- guarded by the SAME reconcile_watermark transaction Reconcile already
-- reads/advances, so two concurrent sweeps serialize exactly like they do
-- today. first_git_sha/last_git_sha: the commit this content FIRST appeared
-- at, and the newest sweep that STILL saw it (advanced on every sweep, even
-- one that writes no new history row -- see postgres/app.go).
CREATE TABLE app_manifest_history (
    app_manifest_history_id  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id                  UUID NOT NULL REFERENCES app (app_id),
    app_manifest_id           UUID NOT NULL REFERENCES app_manifest (app_manifest_id),
    valid_from                 TIMESTAMPTZ NOT NULL,
    valid_to                    TIMESTAMPTZ,
    first_git_sha                TEXT NOT NULL,
    last_git_sha                  TEXT NOT NULL
);

-- Backs v_current_app's point lookup below -- the whole performance point of
-- this migration: was a LEFT JOIN LATERAL scanning a monotonically growing
-- index, now a partial-index point lookup that stays O(1) regardless of how
-- much history accumulates.
CREATE UNIQUE INDEX app_manifest_history_current_idx
    ON app_manifest_history (owner_id) WHERE valid_to IS NULL;

CREATE TABLE chart_manifest_history (
    chart_manifest_history_id  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id                    UUID NOT NULL REFERENCES chart (chart_id),
    chart_manifest_id           UUID NOT NULL REFERENCES chart_manifest (chart_manifest_id),
    valid_from                   TIMESTAMPTZ NOT NULL,
    valid_to                      TIMESTAMPTZ,
    first_git_sha                  TEXT NOT NULL,
    last_git_sha                    TEXT NOT NULL
);

CREATE UNIQUE INDEX chart_manifest_history_current_idx
    ON chart_manifest_history (owner_id) WHERE valid_to IS NULL;

-- ============================================================================
-- 3. app_manifest_release / chart_manifest_release: release-time observations
-- ============================================================================

-- Written ONLY by AssertApps (postgres/app.go's
-- recordAppManifestRelease/recordChartManifestRelease), from any ref. Keeps
-- resolveManifestForPublish's "prefer the exact build commit" fast path
-- working -- see postgres/artifact.go. One row per (owner, git_sha) ever
-- asserted, regardless of whether that commit's content also appears in
-- *_history (a release from a divergent branch is disjoint from main's
-- timeline; a release from a commit main also swept is fine to have in both
-- -- they'll usually share the same app_manifest_id).
--
-- Surrogate UUID PK added for consistency with every other table in this
-- schema (issue #587's SQL sketch omits one) -- not load-bearing, just this
-- schema's convention.
CREATE TABLE app_manifest_release (
    app_manifest_release_id  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id                  UUID NOT NULL REFERENCES app (app_id),
    app_manifest_id           UUID NOT NULL REFERENCES app_manifest (app_manifest_id),
    git_sha                     TEXT NOT NULL,
    recorded_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (owner_id, git_sha)
);

CREATE TABLE chart_manifest_release (
    chart_manifest_release_id  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id                    UUID NOT NULL REFERENCES chart (chart_id),
    chart_manifest_id           UUID NOT NULL REFERENCES chart_manifest (chart_manifest_id),
    git_sha                       TEXT NOT NULL,
    recorded_at                    TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (owner_id, git_sha)
);

-- ============================================================================
-- 4. reconcile_run: one row per sweep, replacing the per-app-per-sweep record
-- ============================================================================

-- Written by postgres/app.go's Reconcile, once per call, ONLY when it
-- actually applies (not dry-run, not SkippedStale by the watermark) -- see
-- reconcile_watermark's own "a dry run never writes" invariant, which this
-- table follows exactly. Answers "which commits were reconciled," which
-- reconcile_watermark alone (a singleton with no history) never could.
CREATE TABLE reconcile_run (
    reconcile_run_id      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    git_sha                 TEXT NOT NULL,
    source_committed_at      BIGINT NOT NULL DEFAULT 0,
    applied_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    apps_seen                    INT NOT NULL,
    charts_seen                    INT NOT NULL
);

-- ============================================================================
-- 5. Rename the old per-commit snapshot tables out of the way
-- ============================================================================

-- Kept around, under a new name, only for the backfill steps below --
-- dropped at the end of this migration (step 9). Renaming (not dropping
-- first) is what lets steps 6-8 read the pre-migration data.
ALTER TABLE app_manifest RENAME TO app_manifest_old;
ALTER TABLE chart_manifest RENAME TO chart_manifest_old;

-- ============================================================================
-- 6. Backfill content tables, deduped by JSONB value (not a hand-computed
--    hash -- Postgres's own equality operator is authoritative here)
-- ============================================================================

INSERT INTO app_manifest (owner_id, manifest_json, first_recorded_at)
SELECT owner_id, manifest_json, MIN(recorded_at)
FROM app_manifest_old
GROUP BY owner_id, manifest_json;

INSERT INTO chart_manifest (owner_id, manifest_json, first_recorded_at)
SELECT owner_id, manifest_json, MIN(recorded_at)
FROM chart_manifest_old
GROUP BY owner_id, manifest_json;

-- ============================================================================
-- 7. Backfill app_manifest_history / chart_manifest_history from old
--    provenance='sweep' rows, via the window-function pattern AGENTS.md
--    prescribes for deriving SCD2 over a non-SCD2 table
-- ============================================================================

-- event_time uses commit time (to_timestamp(source_committed_at)), not
-- recorded_at/NOW() -- otherwise "value at T" answers "when did CI run"
-- instead of "what was in the tree." Falls back to recorded_at (wall clock)
-- ONLY for the source_committed_at = 0 sentinel (migration 006's/008's
-- backfill's "no watermark yet" convention) -- noted here explicitly per
-- issue #587's own callout.
--
-- A new "run" starts whenever content differs from the IMMEDIATELY
-- PRECEDING row for that owner (LAG + a running SUM used as a run id) -- key
-- to A -> B -> A producing THREE runs (non-adjacent repeats of the same
-- content do not merge), not one. Each run collapses to one interval:
-- valid_from = the run's earliest event_time, first_git_sha/last_git_sha =
-- the git_sha of the run's earliest/latest row. valid_to per owner is the
-- NEXT run's valid_from (LEAD), NULL for the newest run (still current).
WITH app_sweep AS (
    SELECT
        old.owner_id,
        old.source_git_sha,
        CASE WHEN old.source_committed_at = 0 THEN old.recorded_at
             ELSE to_timestamp(old.source_committed_at) END AS event_time,
        new_am.app_manifest_id AS content_id
    FROM app_manifest_old old
    JOIN app_manifest new_am
      ON new_am.owner_id = old.owner_id AND new_am.manifest_json = old.manifest_json
    WHERE old.provenance = 'sweep'
),
app_ordered AS (
    SELECT *,
        LAG(content_id) OVER w AS prev_content_id
    FROM app_sweep
    WINDOW w AS (PARTITION BY owner_id ORDER BY event_time, source_git_sha)
),
app_runs AS (
    SELECT *,
        SUM(CASE WHEN prev_content_id IS NULL OR prev_content_id != content_id THEN 1 ELSE 0 END)
            OVER (PARTITION BY owner_id ORDER BY event_time, source_git_sha
                  ROWS UNBOUNDED PRECEDING) AS run_id
    FROM app_ordered
),
app_intervals AS (
    SELECT owner_id, content_id, run_id,
        MIN(event_time) AS valid_from,
        (ARRAY_AGG(source_git_sha ORDER BY event_time))[1] AS first_git_sha,
        (ARRAY_AGG(source_git_sha ORDER BY event_time DESC))[1] AS last_git_sha
    FROM app_runs
    GROUP BY owner_id, content_id, run_id
)
INSERT INTO app_manifest_history (owner_id, app_manifest_id, valid_from, valid_to, first_git_sha, last_git_sha)
SELECT owner_id, content_id, valid_from,
    LEAD(valid_from) OVER (PARTITION BY owner_id ORDER BY valid_from),
    first_git_sha, last_git_sha
FROM app_intervals;

WITH chart_sweep AS (
    SELECT
        old.owner_id,
        old.source_git_sha,
        CASE WHEN old.source_committed_at = 0 THEN old.recorded_at
             ELSE to_timestamp(old.source_committed_at) END AS event_time,
        new_cm.chart_manifest_id AS content_id
    FROM chart_manifest_old old
    JOIN chart_manifest new_cm
      ON new_cm.owner_id = old.owner_id AND new_cm.manifest_json = old.manifest_json
    WHERE old.provenance = 'sweep'
),
chart_ordered AS (
    SELECT *,
        LAG(content_id) OVER w AS prev_content_id
    FROM chart_sweep
    WINDOW w AS (PARTITION BY owner_id ORDER BY event_time, source_git_sha)
),
chart_runs AS (
    SELECT *,
        SUM(CASE WHEN prev_content_id IS NULL OR prev_content_id != content_id THEN 1 ELSE 0 END)
            OVER (PARTITION BY owner_id ORDER BY event_time, source_git_sha
                  ROWS UNBOUNDED PRECEDING) AS run_id
    FROM chart_ordered
),
chart_intervals AS (
    SELECT owner_id, content_id, run_id,
        MIN(event_time) AS valid_from,
        (ARRAY_AGG(source_git_sha ORDER BY event_time))[1] AS first_git_sha,
        (ARRAY_AGG(source_git_sha ORDER BY event_time DESC))[1] AS last_git_sha
    FROM chart_runs
    GROUP BY owner_id, content_id, run_id
)
INSERT INTO chart_manifest_history (owner_id, chart_manifest_id, valid_from, valid_to, first_git_sha, last_git_sha)
SELECT owner_id, content_id, valid_from,
    LEAD(valid_from) OVER (PARTITION BY owner_id ORDER BY valid_from),
    first_git_sha, last_git_sha
FROM chart_intervals;

-- ============================================================================
-- 8. Backfill *_release tables from old provenance='release' rows
-- ============================================================================

-- app_manifest_old's UNIQUE (owner_id, source_git_sha) already guarantees no
-- overlap with step 7's 'sweep' rows -- a given commit is recorded as
-- exactly one provenance, never both.
INSERT INTO app_manifest_release (owner_id, app_manifest_id, git_sha, recorded_at)
SELECT old.owner_id, new_am.app_manifest_id, old.source_git_sha, old.recorded_at
FROM app_manifest_old old
JOIN app_manifest new_am
  ON new_am.owner_id = old.owner_id AND new_am.manifest_json = old.manifest_json
WHERE old.provenance = 'release';

INSERT INTO chart_manifest_release (owner_id, chart_manifest_id, git_sha, recorded_at)
SELECT old.owner_id, new_cm.chart_manifest_id, old.source_git_sha, old.recorded_at
FROM chart_manifest_old old
JOIN chart_manifest new_cm
  ON new_cm.owner_id = old.owner_id AND new_cm.manifest_json = old.manifest_json
WHERE old.provenance = 'release';

-- ============================================================================
-- 9. Remap artifact.manifest_id: old per-commit snapshot id -> new content id
-- ============================================================================

-- artifact.manifest_id is polymorphic (no FK -- migration 008's comment #4),
-- so this must branch on artifact.kind. Many old snapshot ids collapse to
-- one new content id (that IS the point of content-addressing) -- this is
-- the risky step the issue itself flags for review: it changes what every
-- pre-migration artifact row's manifest_id points at, in place.
UPDATE artifact a
SET manifest_id = new_am.app_manifest_id
FROM app_manifest_old old_am
JOIN app_manifest new_am
  ON new_am.owner_id = old_am.owner_id AND new_am.manifest_json = old_am.manifest_json
WHERE a.kind = 'image' AND a.manifest_id = old_am.app_manifest_id;

UPDATE artifact a
SET manifest_id = new_cm.chart_manifest_id
FROM chart_manifest_old old_cm
JOIN chart_manifest new_cm
  ON new_cm.owner_id = old_cm.owner_id AND new_cm.manifest_json = old_cm.manifest_json
WHERE a.kind = 'chart' AND a.manifest_id = old_cm.chart_manifest_id;

-- artifact rows with manifest_id IS NULL (pre-migration-008 artifacts, per
-- that migration's own backfill comment) are untouched -- there was nothing
-- honest to remap them to before, and that has not changed.

-- ============================================================================
-- 10. Rebuild v_current_app: identity <-> CURRENT history row <-> content
-- ============================================================================

-- Byte-identical column names/order to migration 008's v_current_app -- only
-- the join changes, from a LEFT JOIN LATERAL ... ORDER BY ... LIMIT 1 scan
-- to a partial-index point lookup (app_manifest_history_current_idx above).
-- v_current_chart needs NO change: migration 008's version never joined
-- chart_manifest to begin with (chart.deploy_unit/description/
-- chart_repository are hardcoded constants, not manifest-sourced) -- see
-- ARCHITECTURE.md "Why chart_manifest has no generated columns".
DROP VIEW v_current_app;

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
LEFT JOIN app_manifest_history h
    ON h.owner_id = a.app_id AND h.valid_to IS NULL
LEFT JOIN app_manifest m
    ON m.app_manifest_id = h.app_manifest_id;

-- ============================================================================
-- 11. Drop the old per-commit snapshot tables (cascades their indexes)
-- ============================================================================

DROP TABLE app_manifest_old;
DROP TABLE chart_manifest_old;
