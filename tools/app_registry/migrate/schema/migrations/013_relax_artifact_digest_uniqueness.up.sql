-- App Registry — Relax artifact digest uniqueness to allow multi-version same-digest releases (issue #784)
--
-- In OCI and Git registries, multiple release tags (e.g. v0.1.5 and v0.2.0) can point to
-- the same byte-identical content digest (routine in monorepos with hermetic builds).
-- Replaces the UNIQUE index on artifact(digest) with a standard non-unique index.
-- Strict version uniqueness remains enforced by UNIQUE (owner_id, kind, version).

DROP INDEX IF EXISTS artifact_digest_idx;
CREATE INDEX artifact_digest_idx ON artifact (digest) WHERE digest IS NOT NULL;
