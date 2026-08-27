-- Migration 032: FR45's database-level guard on device_config's ack columns
--
-- 032 is the next free number after 031 (device_config_removal); state in
-- the PR since sibling v2 branches on plan/1166 have collided on migration
-- numbers before.
--
-- FR45: accepted / acked_at / rejection_reason are written only from the
-- device's own ack path -- leaflab/processor's AckDeviceConfig
-- (handler.go's handleConfigAck). No API caller, in any role, can set
-- them. Application-only enforcement (no leaflab/api repository method
-- writes these columns) is not sufficient for a requirement worded "in any
-- role" -- this migration adds the database-level half.
--
-- The API and processor share one DB role in this deployment (PG_DATABASE_URL
-- / DB_USER -- see migration 016_audit_log's REVOKE note for the same
-- observation), so a column-privilege GRANT/REVOKE cannot distinguish "the
-- processor's ack write path" from "any other write": both run as the same
-- role. Instead: AckDeviceConfig sets a transaction-local marker (SET LOCAL
-- leaflab.ack_write = 'on') immediately before its UPDATE, inside the same
-- transaction as that UPDATE; a BEFORE UPDATE trigger on device_config
-- raises unless that marker is set for the current transaction. SET
-- LOCAL's scope is exactly one transaction -- it resets at COMMIT/ROLLBACK
-- -- so no other write path, including a same-role UPDATE issued from a
-- different transaction (an elevated admin's ad-hoc statement included),
-- can carry the exemption forward.
--
-- Only a change to one of the three ack columns is guarded: an UPDATE that
-- leaves all three unchanged (none exist on device_config today, but the
-- guard should not gratuitously block a legitimate future one) passes
-- through untouched.

CREATE FUNCTION enforce_device_config_ack_write_guard() RETURNS TRIGGER AS $$
BEGIN
    IF (NEW.accepted IS DISTINCT FROM OLD.accepted
        OR NEW.acked_at IS DISTINCT FROM OLD.acked_at
        OR NEW.rejection_reason IS DISTINCT FROM OLD.rejection_reason)
       AND coalesce(current_setting('leaflab.ack_write', true), '') <> 'on' THEN
        RAISE EXCEPTION 'device_config.accepted/acked_at/rejection_reason are writable only from the device ack path (FR45)';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_device_config_ack_write_guard
    BEFORE UPDATE ON device_config
    FOR EACH ROW
    EXECUTE FUNCTION enforce_device_config_ack_write_guard();
