-- Migration 035: config_derived_from -- FR40 rollback's "what it was derived from"
--
-- 035 is the next free number verified against every plan/1166-v2-*
-- worktree on disk at authoring time (highest seen: 034, on this same
-- plan/1166-v2-1372 branch, inherited from plan/1166-v2-1371) -- see 028's
-- own note on why this is checked here rather than assumed.
--
-- FR40: rollback re-pushes a prior version's complete stored payload as a
-- new higher version, rather than mutating the version rolled back to.
-- derived_from_version records which version a rollback-produced version
-- came from -- NULL for every version pushed by an ordinary
-- PushDeviceConfig call (there is nothing to derive from), set to the
-- to_version named on a RollbackDeviceConfig call for the new version that
-- call inserts.
--
-- Modeled as a composite self-FK against device_config's own
-- (board_id, version) unique key -- not a bare device_config.config_id
-- reference -- because the caller-facing and audited quantity (FR40,
-- FR8) is the *version number* rolled back to, scoped to the same board
-- the new row belongs to; a bare config_id FK would let the column
-- silently reference a version on some *other* board, which is never a
-- meaningful rollback target.
--
-- Scaffold added the column and its lookup index (below). NFR6.2's
-- append-only enforcement -- a BEFORE UPDATE trigger over the columns that
-- define a version's payload, matching 016_audit_log's pattern, scoped so
-- the device-ack columns (accepted, acked_at, rejection_reason) remain
-- updatable -- is the Implementation-phase addition to this same migration
-- file, per 016_audit_log's own scaffold-then-feat precedent, rather than
-- as a separate migration number; see its own comment further down.

ALTER TABLE device_config
    ADD COLUMN derived_from_version BIGINT NULL,
    ADD CONSTRAINT fk_device_config_derived_from_version
        FOREIGN KEY (board_id, derived_from_version)
        REFERENCES device_config (board_id, version);

-- "was this version produced by a rollback, and if so from what" --
-- FR40's own read path, and the audit trail's cross-reference.
CREATE INDEX idx_device_config_derived_from_version
    ON device_config (board_id, derived_from_version)
    WHERE derived_from_version IS NOT NULL;

-- ── Append-only enforcement (NFR6.2) ─────────────────────────────────────
-- Implementation-phase half of this migration, per the scaffold's own note
-- above and 016_audit_log's scaffold-then-feat precedent.
--
-- Unlike 016_audit_log's trigger (which forbids UPDATE unconditionally),
-- this one must let the device-ack path keep working: leaflab/processor's
-- AckDeviceConfig (leaflab/processor/repository.go) legitimately updates
-- accepted/acked_at/rejection_reason on an existing row after the device
-- responds. Every other column defines this version's payload/identity
-- (board_id, version, config_json, pushed_at, push_group_id,
-- derived_from_version) and must never be mutated once written -- FR40's
-- "rollback writes forward" exists specifically so a correction is a new
-- row, never an UPDATE of an old one. DELETE is forbidden unconditionally;
-- nothing in this codebase ever deletes a device_config row.
CREATE FUNCTION enforce_device_config_append_only() RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'device_config is append-only (NFR6.2): DELETE is not permitted (config_id %)', OLD.config_id;
    END IF;

    IF NEW.board_id IS DISTINCT FROM OLD.board_id
        OR NEW.version IS DISTINCT FROM OLD.version
        OR NEW.config_json IS DISTINCT FROM OLD.config_json
        OR NEW.pushed_at IS DISTINCT FROM OLD.pushed_at
        OR NEW.push_group_id IS DISTINCT FROM OLD.push_group_id
        OR NEW.derived_from_version IS DISTINCT FROM OLD.derived_from_version
    THEN
        RAISE EXCEPTION 'device_config is append-only (NFR6.2): only accepted/acked_at/rejection_reason may be updated (config_id %)', OLD.config_id;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_device_config_append_only
    BEFORE UPDATE OR DELETE ON device_config
    FOR EACH ROW
    EXECUTE FUNCTION enforce_device_config_append_only();
