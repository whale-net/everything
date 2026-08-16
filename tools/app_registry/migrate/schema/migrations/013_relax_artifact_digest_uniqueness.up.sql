-- App Registry — Allow shared digest across distinct major/minor versions while keeping digest unique per (owner, kind, major, minor) (issue #784)
--
-- Prevents redundant patch releases from recording identical digests within the same minor release,
-- while allowing major or minor version bumps (e.g. v0.1.5 -> v0.2.0) to share content digests to establish new version baselines.

DROP INDEX IF EXISTS artifact_digest_idx;
CREATE UNIQUE INDEX artifact_digest_major_minor_idx ON artifact (owner_id, kind, version_major, version_minor, digest) WHERE digest IS NOT NULL;
CREATE INDEX artifact_digest_idx ON artifact (digest) WHERE digest IS NOT NULL;
