-- Down migration 018: Remove board retired-state columns

ALTER TABLE board DROP COLUMN IF EXISTS retired_principal;
ALTER TABLE board DROP COLUMN IF EXISTS retired_operation;
ALTER TABLE board DROP COLUMN IF EXISTS retired_at;
