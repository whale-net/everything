-- Remove host public address from servers

ALTER TABLE servers DROP COLUMN IF EXISTS host_public_address;
