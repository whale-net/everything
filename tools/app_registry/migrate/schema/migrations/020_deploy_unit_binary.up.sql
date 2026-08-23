-- App Registry — Add 'binary' deploy_unit for standalone CLI/binary apps
--
-- release_helper_go and app-registry (the CLI tools) are distributed as raw
-- binaries via S3, not container images or Helm charts. They previously
-- declared deploy_unit = "image" purely so DerivePromotability would mark
-- their artifacts promotable -- but ArtifactKindBinary artifacts are already
-- unconditionally promotable regardless of ownerDeployUnit (see
-- promotability.go), so "image" was never accurate, just expedient.
-- app_manifest.deploy_unit's generated expression (migration 010) only
-- recognized DEPLOY_UNIT_IMAGE/NONE, silently mapping any other value
-- (including a future DEPLOY_UNIT_BINARY) to 'chart' via its ELSE branch --
-- must be extended before any manifest can actually carry
-- 'DEPLOY_UNIT_BINARY'.
--
-- Postgres has no ALTER COLUMN ... SET EXPRESSION for a GENERATED column --
-- drop and re-add, same shape as this column's definition in migration 010.

ALTER TABLE app_manifest DROP COLUMN deploy_unit;
ALTER TABLE app_manifest ADD COLUMN deploy_unit TEXT GENERATED ALWAYS AS (
    CASE manifest_json ->> 'deploy_unit'
        WHEN 'DEPLOY_UNIT_IMAGE'  THEN 'image'
        WHEN 'DEPLOY_UNIT_NONE'   THEN 'none'
        WHEN 'DEPLOY_UNIT_BINARY' THEN 'binary'
        ELSE 'chart'
    END
) STORED;
