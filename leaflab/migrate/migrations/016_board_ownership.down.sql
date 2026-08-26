-- Down migration 016: Remove board_ownership SCD2 table and household_id from board

DROP TABLE IF EXISTS board_ownership;
ALTER TABLE board DROP COLUMN IF EXISTS household_id;
