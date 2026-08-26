-- Migration 019: Append-only enforcement for config and audit logs
--
-- Creates trigger to enforce append-only semantics on device_config and audit
-- tables. Per NFR6.2, append-only is enforced as close to the data as practical
-- (database trigger), not only in application code.
--
-- Prevents UPDATE and DELETE on device_config; only INSERT and SELECT allowed.

-- ── Append-only trigger for device_config ────────────────────────────────────

CREATE OR REPLACE FUNCTION enforce_append_only_device_config()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        RAISE EXCEPTION 'device_config is append-only: % not allowed', TG_OP;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trigger_device_config_append_only
    BEFORE UPDATE OR DELETE ON device_config
    FOR EACH ROW
    EXECUTE FUNCTION enforce_append_only_device_config();

-- Note: Audit log table append-only trigger(s) will be added when audit log
-- tables are created. This establishes the pattern for other append-only tables.
