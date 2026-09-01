-- Add optional host public address to servers
-- Nullable, no default, no backfill: absent by default for all existing rows

ALTER TABLE servers ADD COLUMN host_public_address TEXT;
