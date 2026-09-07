-- Reverse migration 019: drop video_script.edit_idempotency_key. Purely
-- additive column with no dependent view/index of its own, so a plain
-- DROP COLUMN is the exact inverse of the up migration.
ALTER TABLE video_script DROP COLUMN edit_idempotency_key;
