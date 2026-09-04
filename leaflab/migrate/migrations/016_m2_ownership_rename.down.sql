-- Reverse migration 016: drop the two sensor columns, then
-- leaflab_user_role (grants go with it), then board.name, in reverse
-- order of the up migration. No data preservation is expected on a down
-- migration here.

ALTER TABLE sensor DROP COLUMN IF EXISTS corrective_push_outstanding_version;
ALTER TABLE sensor DROP COLUMN IF EXISTS corrective_push_attempts;

DROP TABLE IF EXISTS leaflab_user_role;

ALTER TABLE board DROP COLUMN IF EXISTS name;
