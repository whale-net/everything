-- Rollback promotion_sync_event.operation_phase (see .up.sql doc comment).
ALTER TABLE promotion_sync_event
    DROP COLUMN operation_phase;
