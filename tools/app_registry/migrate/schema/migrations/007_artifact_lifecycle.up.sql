-- App Registry — Artifact lifecycle states (AR-7b, issue #558)
--
-- See ARCHITECTURE.md "Release lifecycle (issue #558)" -> "Artifact
-- lifecycle: allocated -> publishing -> published" for the full design.
-- Summary: the registry now records the INTENT to publish an artifact
-- before the push happens (`allocated`/`publishing`), not only the fact
-- that it happened (`published`). This is what makes ordering 3 ("an image
-- must be recorded before any chart pins its digest") a genuinely useful
-- reject instead of a bookkeeping gap, and what makes a killed release run
-- resumable (AR-7d) instead of silently incomplete.
--
-- This migration does five things:
--   1. Adds artifact.state / provenance / version_source / state_changed_at
--      / fail_reason.
--   2. Makes digest / build_id / published_at NULLable -- an `allocated` row
--      has neither a digest nor a build yet; a `publishing` row has a build
--      but no digest.
--   3. Adds a CHECK constraint tying state to which of those three columns
--      may be non-NULL, so an inconsistent row is a schema violation, not
--      just an application bug.
--   4. Narrows artifact_digest_idx to only real (non-NULL) digests -- every
--      `allocated`/`publishing`/`failed` row would otherwise collide on
--      NULL under a naive unique index (Postgres actually treats NULLs as
--      distinct for uniqueness purposes, so this isn't strictly required
--      for correctness, but WHERE digest IS NOT NULL says what is meant:
--      "digest is unique among artifacts that HAVE one" -- and keeps the
--      index smaller by excluding every in-flight row).
--   5. Folds `version_allocation` (AR-5a's reservation ledger) into
--      `artifact` as `state = 'allocated'` rows, then drops the table --
--      see "Fold version_allocation into artifact" below.
--
-- `UNIQUE (owner_id, kind, version)` (artifact_version_idx, migration 001)
-- is UNCHANGED by this migration and now spans every state
-- (allocated/publishing/published/failed all share it) -- it keeps doing
-- exactly the job version_allocation's own unique index used to do
-- (the allocation-collision guard AllocateVersion's concurrency test
-- depends on), just against one table instead of two. "Next version" also
-- collapses from a MAX() across two tables into one query against
-- `artifact` alone -- see server/repository/postgres/artifact.go.

-- ============================================================================
-- 1 + 2. New columns; existing columns become nullable
-- ============================================================================

-- Added nullable first (matching migration 005's ADD-COLUMN-then-backfill
-- style) so the backfill UPDATE below can populate every pre-existing row
-- before NOT NULL is enforced.
ALTER TABLE artifact ADD COLUMN state TEXT
    CHECK (state IN ('allocated', 'publishing', 'published', 'failed'));

ALTER TABLE artifact ADD COLUMN provenance TEXT
    CHECK (provenance IN ('observed', 'adopted'));

-- version_source: which path authored this row's version. 'registry' means
-- AllocateVersion (AR-5) reserved it; 'tag' means the pre-cutover git-tag
-- path chose it and the registry is merely recording the intent (see
-- ARCHITECTURE.md "The run log" -- the release plan step writes this row at
-- EVERY adoption stage, not only 'allocate'). This is what turns AR-5's
-- parity exit criterion ("allocated versions match what tag-scanning would
-- have produced") into a query instead of a manual soak-log comparison.
-- Deliberately no default -- unlike `provenance` (where 'observed' is a
-- safe assumption for any row this migration doesn't otherwise know
-- about), there is no safe default for "who authored this version": every
-- write path (RecordArtifact's direct-create fallback, AllocateVersion,
-- BeginPublish) must set it explicitly, and a forgotten value should be a
-- NOT NULL violation, not a silently wrong guess.
ALTER TABLE artifact ADD COLUMN version_source TEXT
    CHECK (version_source IN ('registry', 'tag'));

-- state_changed_at: when `state` last transitioned. The reaper
-- (app-registry-worker) sweeps on this column -- see artifact_state_idx
-- below.
ALTER TABLE artifact ADD COLUMN state_changed_at TIMESTAMPTZ;

-- fail_reason: set by FailPublish (caller-supplied) or the reaper
-- (hardcoded "stale" -- see ARCHITECTURE.md "The reaper is not optional").
-- A plain TEXT column with a safe empty-string default, not an enum: unlike
-- state/provenance/version_source this is free-text for operator
-- diagnosis, not a value application code branches on.
ALTER TABLE artifact ADD COLUMN fail_reason TEXT NOT NULL DEFAULT '';

-- digest/build_id/published_at: an `allocated` row has neither a digest nor
-- a build yet (AllocateVersionRequest carries neither -- see migration
-- 005's comments, now folded into this table); a `publishing` row has a
-- build but not yet a digest (BeginPublish runs before the push that
-- produces one). DROP DEFAULT on published_at too: it must only ever be
-- set at the instant a row actually becomes 'published' (RecordArtifact),
-- never silently defaulted to NOW() by an INSERT that forgot it -- that
-- would fabricate a publish time for a row that was never published.
ALTER TABLE artifact ALTER COLUMN digest DROP NOT NULL;
ALTER TABLE artifact ALTER COLUMN build_id DROP NOT NULL;
ALTER TABLE artifact ALTER COLUMN published_at DROP NOT NULL;
ALTER TABLE artifact ALTER COLUMN published_at DROP DEFAULT;

-- ============================================================================
-- Backfill existing `artifact` rows
-- ============================================================================

-- Every row that exists before this migration was written by the
-- pre-AR-7b RecordArtifact, which only ever created a row directly in what
-- is now called the 'published' state, always with a real digest/build_id,
-- always via the git-tag version path (AR-5a's AllocateVersion is inert --
-- see PLAN.md's AR-5a status: no domain has ever been at adoption stage
-- 'allocate', so no pre-existing artifact row was ever authored by the
-- registry). provenance = 'observed' because every one of these rows was
-- recorded from a real CI push, not backfilled/adopted after the fact (see
-- ARCHITECTURE.md "Resolved questions" #3 -- there is no bulk backfill in
-- this repo's history to date). state_changed_at backfills to
-- published_at: for a 'published' row, "when did it last change state" and
-- "when was it published" are the same instant, since this migration is the
-- first time `state` exists at all.
UPDATE artifact SET
    state = 'published',
    provenance = 'observed',
    version_source = 'tag',
    state_changed_at = published_at
WHERE state IS NULL;

-- ============================================================================
-- 5. Fold version_allocation into artifact, then drop it
-- ============================================================================

-- version_allocation is empty in every deployed environment as of this
-- migration -- AR-5a shipped AllocateVersion fully implemented but wired to
-- nothing (no domain_adoption row has ever been set to 'allocate'; see
-- PLAN.md's AR-5a status and ARCHITECTURE.md "Relationship to AR-5
-- (allocation cutover)"). That makes this INSERT...SELECT a no-op against
-- real data today -- this is exactly the cheap moment ARCHITECTURE.md calls
-- out for doing this re-homing ("after a cutover, this migration would be
-- folding rows a live release path is writing"). The statement is still
-- written to be correct for a non-empty table, both so the migration is
-- well-defined regardless of what a given environment's actual state is,
-- and so this SQL keeps working if it is ever copied as a reference for a
-- similar fold-in.
--
-- version_allocation carries no `repository` value (AllocateVersionRequest
-- never did either -- see migration 005's comments: allocation happens
-- before a build, and originally before `artifact.repository` needed
-- populating at all). `artifact.repository` is NOT NULL, so a folded row
-- needs one. Derived here via a LEFT JOIN back to the owning app/chart's
-- own stored repository (image_repository / chart_repository) -- the same
-- value RecordArtifact would have used had this owner gone on to publish
-- normally. The COALESCE(..., '') tail is a defensive fallback for an
-- owner row that has since been deleted or somehow carries an empty
-- repository itself; given the table is empty today, this fallback is
-- unreachable in practice, but the column being NOT NULL means the
-- migration must still be well-defined if it ever isn't.
INSERT INTO artifact (
    artifact_id, kind, app_id, chart_id, repository, version,
    version_major, version_minor, version_patch,
    state, provenance, version_source, state_changed_at
)
SELECT
    va.version_allocation_id,
    va.kind,
    CASE WHEN va.kind = 'image' THEN va.owner_id END,
    CASE WHEN va.kind = 'chart' THEN va.owner_id END,
    COALESCE(app.image_repository, chart.chart_repository, ''),
    va.version,
    va.version_major, va.version_minor, va.version_patch,
    'allocated', 'observed', 'registry', va.allocated_at
FROM version_allocation va
LEFT JOIN app   ON va.kind = 'image' AND app.app_id     = va.owner_id
LEFT JOIN chart ON va.kind = 'chart' AND chart.chart_id = va.owner_id;

DROP TABLE version_allocation;

-- ============================================================================
-- Enforce NOT NULL now that every row (backfilled + folded) has a value
-- ============================================================================

ALTER TABLE artifact ALTER COLUMN state SET NOT NULL;

-- provenance gets a DEFAULT: 'observed' is a safe assumption for any future
-- INSERT that forgets to set it explicitly -- unlike state/version_source,
-- where no such safe default exists (see version_source's comment above).
ALTER TABLE artifact ALTER COLUMN provenance SET NOT NULL;
ALTER TABLE artifact ALTER COLUMN provenance SET DEFAULT 'observed';

ALTER TABLE artifact ALTER COLUMN version_source SET NOT NULL;

ALTER TABLE artifact ALTER COLUMN state_changed_at SET NOT NULL;
ALTER TABLE artifact ALTER COLUMN state_changed_at SET DEFAULT NOW();

-- ============================================================================
-- 3. State/column-presence CHECK constraint
-- ============================================================================

-- Ties `state` to exactly which of digest/build_id may be set, so a
-- corrupt row (e.g. 'published' with no digest) is impossible at the
-- schema layer, not just prevented by application code. Added as a
-- table-level named constraint (rather than inline on one column) because
-- it spans three columns.
ALTER TABLE artifact ADD CONSTRAINT artifact_state_shape CHECK (
    (state = 'allocated'  AND digest IS NULL     AND build_id IS NULL) OR
    (state = 'publishing' AND digest IS NULL     AND build_id IS NOT NULL) OR
    (state = 'published'  AND digest IS NOT NULL AND build_id IS NOT NULL) OR
    (state = 'failed'     AND digest IS NULL)
);

-- ============================================================================
-- 4. Narrow the digest unique index to real digests only
-- ============================================================================

DROP INDEX artifact_digest_idx;
CREATE UNIQUE INDEX artifact_digest_idx ON artifact (digest) WHERE digest IS NOT NULL;

-- ============================================================================
-- Reaper support index
-- ============================================================================

-- The stale-row reaper (app-registry-worker) sweeps
-- "state IN ('allocated','publishing') AND state_changed_at < cutoff" on a
-- timer (see ENV.md's ARTIFACT_REAPER_* variables). A partial index keeps
-- this cheap regardless of how large the `published`/`failed` tail of the
-- table grows -- it only ever indexes the (small, transient) set of
-- in-flight rows.
CREATE INDEX artifact_state_idx ON artifact (state, state_changed_at)
    WHERE state IN ('allocated', 'publishing');

-- Note on artifact_version_idx (owner_id, kind, version), migration 001:
-- deliberately UNCHANGED here. It already spans every row regardless of
-- state (Postgres unique indexes don't care about a `state` column they
-- don't include), so it was already doing double duty as both the
-- published-artifact collision guard AND (after this migration) the
-- allocation-collision guard version_allocation_idx used to provide on its
-- own table. That is precisely what makes AllocateVersion's concurrency
-- test (8 goroutines, zero collisions) keep passing unmodified against the
-- new storage -- the constraint backing it never moved.
