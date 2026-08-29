-- Migration 031: device_config_removal -- FR82.6's missing write-side bookkeeping
--
-- 031 is the next free number verified against every plan/1166-v2-*
-- worktree on disk at authoring time (highest seen: 030, on
-- plan/1166-v2-1349) -- see 028's own note on why this is checked here
-- rather than assumed.
--
-- device_config_entry (migration 028) records, per stored config version,
-- every entry the version *contains* and its FR82.4 provenance. It has no
-- notion of an entry a version *removed* -- so nothing on disk lets the
-- device's ack (leaflab/processor's AckDeviceConfig) know which
-- sensor_hw_history interval(s) to close at the version's accepted-at
-- time (FR82.6: "its hardware-history interval is closed at the
-- accepted-at time of the version that dropped it").
--
-- device_config_removal fixes that: one row per entry an EDIT-scope
-- push's remove list actually dropped (leaflab/api/config.Materialise's
-- Result.Removed), written in the same transaction as the device_config
-- row and its device_config_entry rows (append-only, like both). The key
-- columns are the same FR18 canonical hardware key shape as
-- device_config_entry and sensor_hw_history, so the ack-time processor
-- can resolve the concrete sensor row via the board's own
-- idx_sensor_hw_address key (board_id, i2c_address, sensor_type_id,
-- mux_path) and close its open sensor_hw_history interval.
--
-- Always empty for a COMPLETE-scope push (nothing to record: COMPLETE has
-- no remove list, and an entry simply omitted from the payload is not
-- "removed" by this table's definition -- see the Scaffold/Testing note
-- below and the root issue's own open question about whether a COMPLETE
-- push's implicit removals should someday feed this table too).

CREATE TABLE device_config_removal (
    device_config_removal_id BIGSERIAL PRIMARY KEY,
    config_id      BIGINT NOT NULL REFERENCES device_config(config_id) ON DELETE CASCADE,
    -- Canonical hardware key components (FR18), matching
    -- device_config_entry/sensor_hw_history's i2c_address/mux_path
    -- convention. Unlike device_config_entry, i2c_address is NOT NULL
    -- here: RemoveKey.Match (leaflab/api/config) already refuses a remove
    -- naming the unaddressable i2c_address=0 sentinel entry
    -- (ErrUnaddressableRemove, FR82.4), so a dropped entry always has a
    -- real, resolvable address by the time it reaches this table.
    i2c_address    SMALLINT NOT NULL,
    mux_path       JSONB NOT NULL DEFAULT '[]'::jsonb,
    sensor_type_id BIGINT NOT NULL REFERENCES sensor_type(sensor_type_id),
    -- FR82.4's "which form was used" accounting, mirrored here for the
    -- same reason device_config_entry mirrors provenance: directly
    -- queryable per row rather than re-derived.
    form           TEXT NOT NULL CHECK (form IN ('full_key', 'chip_key'))
);

-- "give me every entry this config version dropped" -- the ack-time
-- processor's lookup key.
CREATE INDEX idx_device_config_removal_config_id ON device_config_removal(config_id);
