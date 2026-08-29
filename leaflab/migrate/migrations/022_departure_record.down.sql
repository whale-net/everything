-- Migration 022 down: reverse the append-only departure record.
-- REVOKE needs no explicit reversal: DROP TABLE is a DDL operation, not
-- gated by the DML privileges the up migration revoked.

DROP TRIGGER IF EXISTS trg_departure_record_append_only ON departure_record;
DROP FUNCTION IF EXISTS enforce_departure_record_append_only();

DROP TABLE IF EXISTS departure_record;
