-- App Registry — Version allocation schema (AR-5a)
--
-- Ships the schema AllocateVersion needs. See PLAN.md's AR-5 addendum
-- ("semver semantics") for the full rationale; summarized inline below.
-- AR-5a wires nothing to this -- no domain_adoption row is set to
-- 'allocate' by this migration, and plan.go's git-tag path is unchanged.
-- This is schema plus a server-side RPC implementation only.

-- 1. artifact.version_major/minor/patch: TEXT ordering is lexical, not
-- numeric -- "v1.9.0" sorts above "v1.10.0" -- so ORDER BY on the existing
-- `version` column cannot answer "what is the latest / next version"
-- correctly. Store the parsed integer triple alongside the text so ordering
-- queries are correct and indexed. UNIQUE (owner_id, kind, version) on the
-- TEXT column (artifact_version_idx, from migration 001) is UNCHANGED --
-- it remains the collision guard; these new columns are for ordering only.
ALTER TABLE artifact ADD COLUMN version_major INTEGER;
ALTER TABLE artifact ADD COLUMN version_minor INTEGER;
ALTER TABLE artifact ADD COLUMN version_patch INTEGER;

-- Backfill existing rows by parsing the TEXT version. A row whose version
-- does not match v<major>.<minor>.<patch> (optionally with a trailing
-- prerelease/build suffix, which is ignored here the same way
-- release_helper_go's incrementVersion strips it before bumping) is a real
-- condition, not a theoretical one -- plan.go's own autoIncrementVersion
-- falls back to "v0.0.1" for tags it can't parse. This migration's decision:
-- backfill an unparseable row to 0/0/0 rather than failing the deploy. 0.0.0
-- sorts before every real release, so an unparseable legacy row can never
-- win a "latest" ORDER BY ... DESC query, and it stays visible/queryable
-- (not deleted, not blocking) rather than being silently dropped. As of this
-- migration no such rows are known to exist in any deployed environment
-- (every artifact recorded via RecordArtifact already passed its
-- v<major>.<minor>.<patch> validation), but the columns are NOT NULL below,
-- so this must be an explicit decision either way.
UPDATE artifact SET
    version_major = COALESCE((regexp_match(version, '^v?(\d+)\.(\d+)\.(\d+)'))[1]::INTEGER, 0),
    version_minor = COALESCE((regexp_match(version, '^v?(\d+)\.(\d+)\.(\d+)'))[2]::INTEGER, 0),
    version_patch = COALESCE((regexp_match(version, '^v?(\d+)\.(\d+)\.(\d+)'))[3]::INTEGER, 0);

ALTER TABLE artifact ALTER COLUMN version_major SET NOT NULL;
ALTER TABLE artifact ALTER COLUMN version_minor SET NOT NULL;
ALTER TABLE artifact ALTER COLUMN version_patch SET NOT NULL;
-- DEFAULT 0 only guards against some future INSERT that forgets to set
-- these explicitly; RecordArtifact and AllocateVersion (see
-- server/repository/postgres/artifact.go) always populate them from the
-- same parse that produces `version`, never relying on this default.
ALTER TABLE artifact ALTER COLUMN version_major SET DEFAULT 0;
ALTER TABLE artifact ALTER COLUMN version_minor SET DEFAULT 0;
ALTER TABLE artifact ALTER COLUMN version_patch SET DEFAULT 0;

-- Load-bearing, not an optimization (see migrate/README.md "Required
-- indexes"): "latest" / "next major/minor/patch" is one indexed lookup with
-- correct numeric ordering. A zero-padded TEXT sort key was rejected (see
-- PLAN.md's addendum) -- it caps each component's width and reads badly in
-- psql.
CREATE INDEX artifact_version_order_idx
    ON artifact (owner_id, kind, version_major DESC, version_minor DESC, version_patch DESC);

-- 2. version_allocation: the reservation ledger AllocateVersion writes to.
-- Deliberately NOT the `artifact` table itself: AllocateVersionRequest
-- carries owner/kind/increment/explicit_version but no digest or build_id
-- (see protos/api_messages_artifact.proto) -- allocation happens before a
-- build exists, and `artifact` requires both NOT NULL. version_allocation
-- is a lightweight table with the same (owner_id, kind, version) collision
-- shape as artifact, so a transactional INSERT against its unique index is
-- what makes concurrent AllocateVersion calls for the same owner unable to
-- collide -- see server/repository/postgres/artifact.go's AllocateVersion
-- for how "next version" is computed as the max across both this table and
-- `artifact` (an allocated-but-not-yet-recorded version must not be handed
-- out twice, and a version already recorded via RecordArtifact must not be
-- re-allocated).
CREATE TABLE version_allocation (
    version_allocation_id  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id                UUID NOT NULL,
    kind                    TEXT NOT NULL CHECK (kind IN ('image', 'chart')),
    version                 TEXT NOT NULL,
    version_major           INTEGER NOT NULL,
    version_minor           INTEGER NOT NULL,
    version_patch           INTEGER NOT NULL,
    allocated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX version_allocation_idx ON version_allocation (owner_id, kind, version);
CREATE INDEX version_allocation_order_idx
    ON version_allocation (owner_id, kind, version_major DESC, version_minor DESC, version_patch DESC);

-- domain_adoption already exists (migration 001, one row per domain,
-- default stage 'observe'); AllocateVersion's per-domain gate
-- (ARCHITECTURE.md "Resolved questions" #3) reads it as-is. No schema
-- change needed here, and no domain is moved to 'allocate' by this
-- migration.
