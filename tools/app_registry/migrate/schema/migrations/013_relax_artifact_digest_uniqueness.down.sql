DROP INDEX IF EXISTS artifact_digest_major_minor_idx;
DROP INDEX IF EXISTS artifact_digest_idx;
CREATE UNIQUE INDEX artifact_digest_idx ON artifact (digest) WHERE digest IS NOT NULL;
