-- Rollback: restore deploy_unit's generated expression to migration 010's
-- 3-way mapping. Lossy for any row whose manifest_json carries
-- 'DEPLOY_UNIT_BINARY' -- it falls back to 'chart' via the restored ELSE
-- branch, same as it did before this migration existed.

ALTER TABLE app_manifest DROP COLUMN deploy_unit;
ALTER TABLE app_manifest ADD COLUMN deploy_unit TEXT GENERATED ALWAYS AS (
    CASE manifest_json ->> 'deploy_unit'
        WHEN 'DEPLOY_UNIT_IMAGE' THEN 'image'
        WHEN 'DEPLOY_UNIT_NONE'  THEN 'none'
        ELSE 'chart'
    END
) STORED;
