-- Rollback promotion_sync_event (issue #1028). Best-effort, matching
-- migration 010/019's own down.sql precedent for append-only logs: this
-- table's history is not depended on by anything the down path needs to
-- reconstruct, so a plain drop is sufficient -- its indexes are dropped
-- implicitly with the table.
DROP TABLE promotion_sync_event;
