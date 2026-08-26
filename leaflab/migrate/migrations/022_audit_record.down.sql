-- Rollback Migration 022: Audit record table

DROP TRIGGER IF EXISTS audit_record_no_modify ON audit_record;
DROP FUNCTION IF EXISTS audit_record_append_only();
DROP TABLE IF EXISTS audit_record;
