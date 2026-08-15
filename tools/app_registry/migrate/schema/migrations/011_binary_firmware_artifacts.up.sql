-- App Registry — Support binary and firmware artifact kinds (issue #703)
--
-- Extends artifact.kind CHECK constraint to accept 'binary' and 'firmware'
-- in addition to 'image' and 'chart'.
-- Updates artifact_owner_matches_kind to tie 'binary' and 'firmware' to app_id.

ALTER TABLE artifact DROP CONSTRAINT artifact_kind_check;
ALTER TABLE artifact ADD CONSTRAINT artifact_kind_check
    CHECK (kind IN ('image', 'chart', 'binary', 'firmware'));

ALTER TABLE artifact DROP CONSTRAINT artifact_owner_matches_kind;
ALTER TABLE artifact ADD CONSTRAINT artifact_owner_matches_kind CHECK (
    (kind IN ('image', 'binary', 'firmware') AND app_id IS NOT NULL AND chart_id IS NULL) OR
    (kind = 'chart' AND chart_id IS NOT NULL AND app_id IS NULL)
);
