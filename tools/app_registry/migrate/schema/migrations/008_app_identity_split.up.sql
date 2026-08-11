-- App Registry — App identity / manifest snapshot split (AR-7c, issue #558)
--
-- See ARCHITECTURE.md "Release lifecycle (issue #558)" -> "App identity vs.
-- per-build manifest snapshot" for the full design. Summary: `app`/`chart`
-- today carry MUTABLE metadata (description, deploy_unit, bazel_label,
-- image_repository, ...) that some ref has to author, and every choice of
-- "which ref" is bad -- let any ref write it and it drifts; let only `main`
-- write it and releases depend on `main`'s CI having gone first (issue
-- #547). This migration removes the question instead of answering it:
--
--   1. `app`/`chart` become pure identity: (domain, name), status,
--      first/last-seen. Nothing mutable survives on these tables.
--   2. The AppManifest/ChartManifest a run was built from is stored
--      VERBATIM (protojson, JSONB) in a new append-only `app_manifest` /
--      `chart_manifest` snapshot table, keyed unique on
--      (owner_id, source_git_sha) -- same commit, same manifest, so writes
--      are naturally idempotent (an ON CONFLICT DO NOTHING in the Go code,
--      not enforced here -- see postgres/app.go). This is deliberately the
--      SINGLE source of truth for app/chart metadata: a new appmeta field
--      needs no migration here and cannot drift from the manifest, because
--      there is nothing beside the manifest left to drift.
--   3. Two STORED GENERATED columns, `deploy_unit` and `image_repository`,
--      ride alongside app_manifest.manifest_json -- ONLY these two, because
--      they are the fields the hot read paths (promotability derivation,
--      RecordBuild/BeginPublish's owner-repository lookup) need indexable.
--      Fully typed columns for the rest of the manifest were explicitly
--      rejected in review of PR #559: they would reintroduce the
--      hand-maintained duplicate of the manifest schema that AR-M spent a
--      whole phase deleting (see ARCHITECTURE.md "Shared manifest schema").
--      chart_manifest gets NEITHER column -- see "Why chart_manifest has no
--      generated columns" below.
--   4. `artifact` gains `manifest_id` (which snapshot a published row was
--      built from) and `promotability`, now a STORED column computed once
--      at publish time from that snapshot instead of re-derived on every
--      read from the owner's CURRENT deploy_unit. This is the correctness
--      fix ARCHITECTURE.md calls out: editing a release_app's deploy_unit
--      today silently changes the promotability of artifacts published
--      years ago (postgres/artifact.go's old scanArtifact joined app/chart
--      LIVE on every SELECT). After this migration promotability is frozen
--      at RecordArtifact time and never recomputed.
--   5. `v_current_app` / `v_current_chart` views pre-join identity ⋈ latest
--      `main`-sweep snapshot, following the exact pattern `v_current_promotion`
--      already established (migration 003) -- every existing read path
--      (ListApps/GetApp/ListCharts/scanApp/scanChart in
--      postgres/app.go) is repointed at these views with NO Go-level
--      column-order change, so the wire shape of App/Chart is unchanged.
--   6. Backfill: one app_manifest/chart_manifest snapshot row per CURRENT
--      app/chart row, and artifact.promotability for every EXISTING
--      artifact row, computed from today's (about-to-be-dropped) app/chart
--      columns -- see "Backfill" below. Nothing loses metadata.
--
-- Why now, not earlier: AR-7b (migration 007) already made digest/build_id
-- nullable and gave artifact a lifecycle; this migration reuses that same
-- write path (BeginPublish -> RecordArtifact) to also resolve and stamp
-- manifest_id/promotability, so it depends on 007 and could not have landed
-- first.

-- ============================================================================
-- 1. app_manifest / chart_manifest: append-only snapshot tables
-- ============================================================================

-- app_manifest: one row per (app, git_sha) a manifest was ever observed at,
-- written by BOTH ReconcileApps (provenance = 'sweep', from `main` only) and
-- the new AssertApps (provenance = 'release', from any ref -- AR-7c). The
-- unique index makes a repeat write of the SAME commit's manifest a no-op
-- (see postgres/app.go's upsertAppManifestSnapshot: `ON CONFLICT
-- (owner_id, source_git_sha) DO NOTHING`) -- there is nothing to update,
-- because the manifest for a given commit cannot change.
CREATE TABLE app_manifest (
    app_manifest_id       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id              UUID NOT NULL REFERENCES app (app_id),
    source_git_sha        TEXT NOT NULL,

    -- Committer timestamp (Unix seconds) of source_git_sha -- same field and
    -- same 0-means-unresolved convention as AppManifestSet.source_committed_at
    -- (appmeta.proto) and reconcile_watermark.source_committed_at (migration
    -- 006). Used to pick the newest sweep snapshot in v_current_app below.
    source_committed_at   BIGINT NOT NULL DEFAULT 0,

    -- Which call wrote this snapshot. 'sweep' = ReconcileApps (main push
    -- only) -- the only provenance v_current_app reads, because absence/
    -- "what does this app look like today" is a main-tree question (see
    -- ARCHITECTURE.md "AssertApps (additive) vs. ReconcileApps"). 'release'
    -- = AssertApps, called from release.yml against whatever ref is being
    -- released -- recorded for provenance/audit ("was this build's
    -- promotability derived from a sweep snapshot or a release-time one?"),
    -- never read by v_current_app.
    provenance            TEXT NOT NULL CHECK (provenance IN ('sweep', 'release')),

    -- The AppManifest, verbatim, as protojson
    -- (UseProtoNames: true, EmitUnpopulated: true -- see postgres/app.go's
    -- marshalAppManifest). This IS the metadata now; description/language/
    -- app_type/bazel_label/etc. all live only here, read back out via
    -- v_current_app's ->> projections. Storing protojson (not a hand-rolled
    -- JSON shape) means a field added to AppManifest needs no migration --
    -- it just starts appearing in manifest_json, exactly like AR-M's
    -- rationale for using the shared appmeta proto in the first place.
    manifest_json          JSONB NOT NULL,

    -- Stored generated columns -- see this migration's header comment #3.
    -- Parses the SAME four-way DeployUnit enum protojson emits
    -- ("DEPLOY_UNIT_CHART"/"DEPLOY_UNIT_IMAGE"/"DEPLOY_UNIT_NONE"/
    -- "DEPLOY_UNIT_UNSPECIFIED" or the key absent) into the lowercase DB
    -- convention app.deploy_unit used to store directly -- mirrors
    -- postgres/app.go's deployUnitToDB/normalizeDeployUnit exactly:
    -- UNSPECIFIED (or a missing key) defaults to 'chart', matching
    -- normalizeDeployUnit's existing default-to-chart behavior.
    deploy_unit            TEXT GENERATED ALWAYS AS (
        CASE manifest_json ->> 'deploy_unit'
            WHEN 'DEPLOY_UNIT_IMAGE' THEN 'image'
            WHEN 'DEPLOY_UNIT_NONE'  THEN 'none'
            ELSE 'chart'
        END
    ) STORED,

    -- Mirrors postgres/app.go's imageRepository() helper exactly: empty
    -- only when registry/organization/repo_name are ALL empty, otherwise
    -- the slash-joined concatenation (even if some parts are empty) -- same
    -- edge-case behavior the pre-AR-7c Go helper had, preserved rather than
    -- "fixed" here so this migration is a pure storage change, not a
    -- behavior change.
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

-- Backs v_current_app's "latest sweep snapshot per owner" lookup below --
-- see this migration's header comment #5.
CREATE INDEX app_manifest_current_idx
    ON app_manifest (owner_id, provenance, source_committed_at DESC, recorded_at DESC);

-- chart_manifest: the chart-side counterpart. See "Why chart_manifest has
-- no generated columns" below for why this table has manifest_json and
-- nothing else beside the standard columns.
CREATE TABLE chart_manifest (
    chart_manifest_id      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id                UUID NOT NULL REFERENCES chart (chart_id),
    source_git_sha          TEXT NOT NULL,
    source_committed_at     BIGINT NOT NULL DEFAULT 0,
    provenance               TEXT NOT NULL CHECK (provenance IN ('sweep', 'release')),
    manifest_json            JSONB NOT NULL,
    recorded_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (owner_id, source_git_sha)
);

CREATE INDEX chart_manifest_current_idx
    ON chart_manifest (owner_id, provenance, source_committed_at DESC, recorded_at DESC);

-- Why chart_manifest has no generated columns: the two columns this
-- migration adds generated columns for (deploy_unit, image_repository) are
-- both APP-only concepts. `ChartManifest` (appmeta.proto) carries no
-- deploy_unit field at all -- a chart's own "deploy_unit" was always the
-- HARDCODED constant DEPLOY_UNIT_CHART in Reconcile's old INSERT (a chart is
-- definitionally the chart deploy unit), never something read from a
-- manifest, and v_current_chart below keeps hardcoding it the same way, so
-- there is nothing on chart_manifest to derive it from. image_repository
-- likewise has no chart-side equivalent (charts don't have an image
-- registry/organization/repo_name triple). Adding either column here would
-- violate the "only these two, and only where the hot path needs them"
-- constraint decided in review of PR #559 -- there is no hot path reading a
-- generated column off chart_manifest.

-- ============================================================================
-- 2. Backfill: one snapshot row per CURRENT app/chart, before the columns
--    those snapshots are sourced from disappear
-- ============================================================================

-- The commit every backfilled snapshot is attributed to: the most recently
-- applied ReconcileApps call, i.e. reconcile_watermark's current git_sha --
-- named explicitly in PLAN.md's AR-7c scope ("Backfill snapshots from the
-- current app/chart rows against the latest reconciled git_sha"). Falls
-- back to the empty-string sentinel (migration 006's "no watermark yet"
-- value) if this environment has literally never run a reconcile, in which
-- case app/chart are expected to be empty anyway and this backfill affects
-- zero rows.
--
-- manifest_json is synthesized from the CURRENT (pre-drop) app columns,
-- shaped exactly like a real protojson-encoded AppManifest -- see this
-- migration's header comment #6 and postgres/app.go's marshalAppManifest,
-- which produces the same key set for every FUTURE write. registry/
-- organization/repo_name are reconstructed by splitting the existing
-- image_repository string on '/' -- safe because imageRepository() (the
-- function that built that string in the first place) always joined
-- exactly those three parts with '/', and none of them legitimately
-- contains a '/' of its own (a GHCR registry host, an org name, and a repo
-- name are each a single path segment).
INSERT INTO app_manifest (owner_id, source_git_sha, source_committed_at, provenance, manifest_json)
SELECT
    a.app_id,
    COALESCE((SELECT git_sha FROM reconcile_watermark WHERE id = 1), ''),
    COALESCE((SELECT source_committed_at FROM reconcile_watermark WHERE id = 1), 0),
    'sweep',
    jsonb_build_object(
        'name', a.name,
        'domain', a.domain,
        'description', a.description,
        'language', a.language,
        'registry', split_part(a.image_repository, '/', 1),
        'organization', split_part(a.image_repository, '/', 2),
        'repo_name', split_part(a.image_repository, '/', 3),
        'binary_target', a.bazel_label,
        'app_type', a.app_type,
        'deploy_unit', CASE a.deploy_unit
            WHEN 'image' THEN 'DEPLOY_UNIT_IMAGE'
            WHEN 'none'  THEN 'DEPLOY_UNIT_NONE'
            ELSE 'DEPLOY_UNIT_CHART'
        END
    )
FROM app a;

-- chart_manifest's backfill has less to work with: `chart` never stored
-- version/namespace/environment/chart_target (those columns never existed
-- on this table -- see migrate/README.md's "Planned migrations"), and
-- chart.description/chart_repository were columns that Reconcile's INSERT
-- never actually populated (always ''  -- see postgres/app.go's chart
-- INSERT, pre-migration). What CAN be backfilled faithfully is identity
-- (name/domain) and composition (app_refs, from the live chart_app join,
-- domain-qualified to match what helm_chart_metadata emits post-AR-7a) --
-- that is what "nothing loses metadata" means here: nothing that was ever
-- actually populated is lost.
INSERT INTO chart_manifest (owner_id, source_git_sha, source_committed_at, provenance, manifest_json)
SELECT
    c.chart_id,
    COALESCE((SELECT git_sha FROM reconcile_watermark WHERE id = 1), ''),
    COALESCE((SELECT source_committed_at FROM reconcile_watermark WHERE id = 1), 0),
    'sweep',
    jsonb_build_object(
        'name', c.name,
        'domain', c.domain,
        'app_refs', COALESCE((
            SELECT jsonb_agg(app.domain || '/' || app.name ORDER BY app.domain, app.name)
            FROM chart_app ca
            JOIN app ON app.app_id = ca.app_id
            WHERE ca.chart_id = c.chart_id
        ), '[]'::jsonb)
    )
FROM chart c;

-- ============================================================================
-- 3. artifact.manifest_id / artifact.promotability
-- ============================================================================

-- manifest_id: which snapshot a published artifact was built from. No FK
-- constraint -- it is a POLYMORPHIC reference (an image artifact's
-- manifest_id points into app_manifest, a chart artifact's into
-- chart_manifest; a single column cannot carry two REFERENCES targets, the
-- same reason `artifact.owner_id` -- migration 001 -- is a plain generated
-- column rather than an FK). Referential integrity here is enforced in Go
-- (postgres/artifact.go always resolves manifest_id from a row it just
-- selected in the same transaction) rather than by the schema, exactly like
-- owner_id already does.
ALTER TABLE artifact ADD COLUMN manifest_id UUID;

-- promotability: STORED, computed ONCE at publish time (RecordArtifact's
-- publishing -> published transition, or the direct-create fallback) from
-- the snapshot manifest_id points at -- see this migration's header comment
-- #4. NULL for allocated/publishing/failed rows (nothing to derive
-- promotability from until a manifest snapshot has actually been resolved,
-- which only happens at publish time -- mirrors digest/build_id's
-- state-gated nullability from migration 007).
ALTER TABLE artifact ADD COLUMN promotability TEXT
    CHECK (promotability IN ('promotable', 'via_chart', 'not_promotable'));

-- Backfill promotability for every EXISTING artifact row (every one of
-- which is 'published' -- see migration 007's own backfill) using the
-- CURRENT (about-to-be-dropped) app/chart.deploy_unit -- the same value the
-- OLD live-join scanArtifact would have computed for these rows today. This
-- is the last time this repo computes promotability by joining to a live
-- deploy_unit; from here on every NEW published row gets promotability
-- computed once by Go code at publish time and stored, never recomputed.
-- manifest_id is deliberately left NULL for these rows: there is no
-- snapshot in app_manifest/chart_manifest that corresponds to what was
-- ACTUALLY live when each of these was originally published (the backfilled
-- snapshot above reflects the state at MIGRATION time, not publish time),
-- and attributing them to it would be dishonest metadata, not a real
-- historical link.
-- Image artifacts: DerivePromotability(IMAGE, app.deploy_unit) --
-- see server/repository/promotability.go. Filtered to state = 'published'
-- deliberately, even though every row that predates migration 007 is
-- already 'published' -- an environment that deployed AR-7b (007) before
-- this migration could have real allocated/publishing/failed rows in
-- flight, and those must keep promotability NULL (nothing has been
-- published yet to derive it from).
UPDATE artifact a
SET promotability = CASE app.deploy_unit
    WHEN 'chart' THEN 'via_chart'
    WHEN 'image' THEN 'promotable'
    ELSE 'not_promotable'
END
FROM app
WHERE a.kind = 'image' AND a.app_id = app.app_id AND a.state = 'published';

-- Chart artifacts: DerivePromotability(CHART, chart.deploy_unit) is always
-- 'promotable', because chart.deploy_unit is always the hardcoded 'chart'
-- constant (see "Why chart_manifest has no generated columns" above) -- no
-- join needed, unlike the image case, since there is no other value it can
-- take today.
UPDATE artifact
SET promotability = 'promotable'
WHERE kind = 'chart' AND state = 'published';

-- Ties promotability's presence to state, the same way migration 007's
-- artifact_state_shape ties digest/build_id to state: a published row
-- always has one (RecordArtifact/insertArtifact's published path always
-- sets it -- see postgres/artifact.go), anything else never does.
ALTER TABLE artifact ADD CONSTRAINT artifact_promotability_shape CHECK (
    (state = 'published' AND promotability IS NOT NULL) OR
    (state != 'published' AND promotability IS NULL)
);

-- ============================================================================
-- 4. app / chart become pure identity
-- ============================================================================

ALTER TABLE app
    DROP COLUMN description,
    DROP COLUMN language,
    DROP COLUMN app_type,
    DROP COLUMN deploy_unit,
    DROP COLUMN bazel_label,
    DROP COLUMN image_repository;

ALTER TABLE chart
    DROP COLUMN description,
    DROP COLUMN chart_repository,
    DROP COLUMN deploy_unit;

-- ============================================================================
-- 5. v_current_app / v_current_chart: identity ⋈ latest `main`-sweep snapshot
-- ============================================================================

-- Follows v_current_promotion's pattern (migration 003) exactly: pre-join
-- so no consumer re-derives it. Column order/names match appColumns in
-- postgres/app.go BEFORE this migration exactly, so scanApp is UNCHANGED --
-- only the FROM clause of every read query moves from `app` to this view.
--
-- Deliberately reads ONLY provenance = 'sweep' snapshots, never 'release'
-- ones: "what does this app look like today" is a main-tree question (see
-- ARCHITECTURE.md "AssertApps (additive) vs. ReconcileApps (absence
-- sweep)") -- a release-time AssertApps snapshot from a divergent/unmerged
-- ref must never leak into what every other reader sees as "the app".
-- LEFT JOIN LATERAL (not a plain join to a MAX(...) subquery) because
-- "newest snapshot" needs every column from the SAME row, not just the
-- newest timestamp.
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

-- chart's deploy_unit/description/chart_repository are constants, not
-- projections off manifest_json -- see "Why chart_manifest has no generated
-- columns" above: none of those three were ever sourced from the manifest
-- to begin with (deploy_unit was hardcoded, description/chart_repository
-- were always ''), so hardcoding them here is not a behavior change, it is
-- the SAME behavior expressed as a view instead of dead INSERT columns.
CREATE VIEW v_current_chart AS
SELECT
    c.chart_id,
    c.domain,
    c.name,
    ''::text     AS description,
    ''::text     AS chart_repository,
    'chart'::text AS deploy_unit,
    c.status,
    c.first_seen_at,
    c.last_seen_at
FROM chart c;
