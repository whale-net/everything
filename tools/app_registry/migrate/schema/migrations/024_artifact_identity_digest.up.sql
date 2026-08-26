-- App Registry — Content identity digest (FR-13/FR-14/FR-16/FR-17, issue #1159)
--
-- Adds artifact.identity_digest column for no-op detection.
-- The identity digest is computed over uncompressed content as a canonical
-- hash of (relative path, size, sha256-of-uncompressed-bytes) triples,
-- sorted by path. It is invariant to compression level, file ordering,
-- archive metadata, and manifest formatting.
--
-- No-op detection (FR-17): when an artifact's identity_digest matches the
-- currently-published version's digest, no new version is allocated and the
-- run reports the existing published version as its result. This occurs
-- BEFORE version allocation.
--
-- identity_digest is NOT NULL for all published/publishing artifacts, but
-- IS NULL for allocated/failed artifacts (computed during build, not at
-- allocation time). A partial unique index backs no-op detection queries.

ALTER TABLE artifact ADD COLUMN identity_digest TEXT;

-- Backfill existing published rows with NULL (they were published before
-- this column existed, and retroactively computing it is not possible)
-- This is safe because no-op detection only applies to new publishes going
-- forward.
UPDATE artifact SET identity_digest = NULL WHERE identity_digest IS NULL;

-- Add partial unique index on (owner_id, kind, identity_digest, version_major, version_minor)
-- to enforce the uniqueness constraint: within a single minor series (vX.Y),
-- content digests (identity) must remain unique. This prevents redundant patch
-- releases with identical content.
-- See ARCHITECTURE.md "Artifact lifecycle" and issue #585.
CREATE UNIQUE INDEX artifact_identity_digest_idx ON artifact
  (owner_id, kind, identity_digest, version_major, version_minor)
  WHERE identity_digest IS NOT NULL AND state = 'published';

-- Add index for no-op detection queries: look up by owner, kind, and identity_digest
-- to find an existing published artifact with matching content.
CREATE INDEX artifact_identity_lookup_idx ON artifact (owner_id, kind, identity_digest)
  WHERE identity_digest IS NOT NULL AND state = 'published';
