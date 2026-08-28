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
-- Scaffold only: this migration adds the column (and its lookup index).
-- NFR6.2's append-only enforcement -- a BEFORE UPDATE trigger over the
-- columns that define a version's payload, matching 016_audit_log's
-- pattern, scoped so the device-ack columns (accepted, acked_at,
-- rejection_reason) remain updatable -- is Implementation-phase work,
-- added to this same migration file (035_config_derived_from.up.sql) per
-- 016_audit_log's own scaffold-then-feat precedent, rather than as a
-- separate migration number.

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
