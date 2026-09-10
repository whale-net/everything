-- Remove host drain state from servers

ALTER TABLE servers DROP CONSTRAINT IF EXISTS servers_drain_state_check;
ALTER TABLE servers DROP COLUMN IF EXISTS drain_state;
ALTER TABLE servers DROP COLUMN IF EXISTS drain_requested_at;
