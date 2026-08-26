-- Migration 022 down: Drop audit log

DROP TRIGGER IF EXISTS trigger_audit_record_append_only ON audit_record;
DROP FUNCTION IF EXISTS enforce_audit_record_append_only();
DROP TABLE IF EXISTS audit_record;
