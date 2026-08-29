-- Migration 034: push_group -- FR48.1/FR48.2's multi-board push bookkeeping
--
-- 034 is the next free number verified against every plan/1166-v2-*
-- worktree on disk at authoring time (highest seen: 033, on several
-- sibling v2 branches) -- see 028's own note on why this is checked here
-- rather than assumed.
--
-- FR48 lets one PushDeviceConfig call target a set of boards ("a
-- multi-board push"). push_group is the append-only record of that one
-- call: the reason the caller stated (FR48.2 -- required for a
-- multi-board push), who pushed it, and when. Every device_config row
-- (migration 007) the call produces -- one per targeted board -- links
-- back to this same push_group_id, which is how GetPushGroupStatus reads
-- the resulting group's per-board ack state (acked/rejected/silent) back
-- as a group after the fact, rather than as N unrelated device_config
-- rows with no shared handle.
--
-- Append-only, like device_config itself -- not SCD2: a push_group row is
-- never superseded or corrected in place, and there is no current/prior
-- distinction to track.
--
-- A single-board push may or may not create a push_group row; that
-- decision belongs to Implementation-phase wiring (leaflab/api/server.go),
-- not to this migration. push_group_id on device_config is nullable
-- either way, so a device_config row pushed before this migration existed
-- (or by a path that never creates a group) is unaffected.

CREATE TABLE push_group (
    push_group_id  BIGSERIAL PRIMARY KEY,
    reason         TEXT NOT NULL,
    actor_subject  TEXT NOT NULL,
    pushed_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE device_config
    ADD COLUMN push_group_id BIGINT REFERENCES push_group(push_group_id);

-- "give me every device_config row (and thus every board) this push_group
-- targeted" -- GetPushGroupStatus's lookup key.
CREATE INDEX idx_device_config_push_group_id
    ON device_config(push_group_id)
    WHERE push_group_id IS NOT NULL;
