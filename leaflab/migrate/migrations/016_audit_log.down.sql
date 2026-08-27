-- Migration 016 down: reverse the append-only audit log.
-- REVOKE needs no explicit reversal: DROP TABLE is a DDL operation, not
-- gated by the DML privileges the up migration revoked.

DROP TRIGGER IF EXISTS trg_audit_log_append_only ON audit_log;
DROP FUNCTION IF EXISTS enforce_audit_log_append_only();

DROP TABLE IF EXISTS audit_log;
