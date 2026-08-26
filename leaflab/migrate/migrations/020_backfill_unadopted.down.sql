-- Down migration 020: Remove backfill of Unadopted ownership
--
-- Clears household_id values and board_ownership history inserted during backfill.
-- Note: This is best-effort; if there have been subsequent writes to these columns,
-- this down migration may not restore the original state perfectly.

-- Clear board_ownership history (backfilled rows only - we can't easily distinguish,
-- so we clear all for safety).
DELETE FROM board_ownership;

-- Clear household_id values on all tables.
UPDATE board SET household_id = NULL;
UPDATE region SET household_id = NULL WHERE parent_region_id IS NULL;
UPDATE plant SET household_id = NULL;
