-- App Registry — Rollback support for binary and firmware artifact kinds (issue #703)

ALTER TABLE artifact DROP CONSTRAINT artifact_owner_matches_kind;
ALTER TABLE artifact ADD CONSTRAINT artifact_owner_matches_kind CHECK (
    (kind = 'image' AND app_id IS NOT NULL AND chart_id IS NULL) OR
    (kind = 'chart' AND chart_id IS NOT NULL AND app_id IS NULL)
);

ALTER TABLE artifact DROP CONSTRAINT artifact_kind_check;
ALTER TABLE artifact ADD CONSTRAINT artifact_kind_check
    CHECK (kind IN ('image', 'chart'));
