-- Migration 019 Rollback: Restore parameter columns
-- Note: This only restores the columns, not the data or views

ALTER TABLE sessions ADD COLUMN IF NOT EXISTS parameters JSONB;
ALTER TABLE server_game_configs ADD COLUMN IF NOT EXISTS parameters JSONB;
ALTER TABLE game_configs ADD COLUMN IF NOT EXISTS parameters JSONB;
