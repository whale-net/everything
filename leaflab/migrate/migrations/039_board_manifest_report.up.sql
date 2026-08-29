-- Migration 035: board_manifest_report -- FR49's "reported inventory" storage
--
-- 035 is the next free number verified against every plan/1166-v2-* and
-- plan/1166-rebuild-trunk worktree on disk under .pm-worktrees/ at
-- authoring time (highest seen: 034, on plan/1166-v2-1378 and
-- plan/1166-v2-1371/#1380 -- see the "Scope note: migration 034 numbering
-- collision" issue) -- see 028's own note on why this is checked here
-- rather than assumed.
--
-- FR49: the board's most recent reported hardware inventory must be
-- readable per device, with the instant it was reported. Nothing on disk
-- today stores that as its own fact -- handleManifest
-- (leaflab/processor/handler.go) upserts each manifest entry directly
-- into sensor/sensor_hw_history, which is a *cumulative* record across
-- every manifest ever received (an entry dropped from a later manifest is
-- never retired there), not "what the last single manifest reported".
-- board.last_seen_at (migration 001) is no substitute either: it updates
-- on every message this board sends (readings, config acks, ...), not
-- specifically on a manifest.
--
-- board_manifest_report is a replace-on-write snapshot of the single most
-- recent DeviceManifest a board sent -- not a history table, not SCD2
-- (NFR6.3 does not apply: there is no current/superseded distinction to
-- preserve, only "the latest one", the same "last value" shape as
-- board.last_seen_at itself). On each manifest, the caller deletes every
-- existing board_manifest_report_entry row for the board and reinserts
-- the manifest's current entries, then stamps reported_at -- mirroring
-- board.last_seen_at's own UPSERT-with-NOW() pattern, but at the
-- per-board-report granularity FR49 needs.
--
-- FR49's "the manifest is a lossy echo of desired state, never a source"
-- is the reason this exists as its own table rather than as a derived
-- read over sensor/sensor_hw_history: an entry ApplyFactory()
-- (firmware/config/config_applier.cc) silently dropped -- unknown chip
-- type, invalid address, no resolvable bus endpoint, exhausted pool --
-- never reaches a manifest at all, so it is definitionally *absent* here.
-- That absence, compared against the stored desired state
-- (device_config/device_config_entry, migration 007/028), is exactly
-- GetConfigDrift's `in_desired_not_reported` classification -- the
-- interesting case the lossy echo reveals. This table is read-only from
-- the materialisation code path's perspective: leaflab/api/config must
-- never import or query it (leaflab/conformance's structural guard
-- asserts this), because an entry that failed to instantiate would
-- otherwise be silently deleted from the stored desired state the next
-- time it's carried forward.
--
-- Column shape mirrors device_config_entry's canonical hardware key
-- (FR18: i2c_address, mux_path, sensor_type) exactly, for the same reason
-- 028 gives: idx_sensor_hw_address, sensor_hw_history and
-- device_config_entry all key on it, and //leaflab/hwkey.Key's
-- SQLPredicate is written against that one shape. name/unit/chip_model
-- are carried verbatim from the wire SensorDescriptor (firmware.proto) --
-- report fields, not resolved/authoritative catalog values -- so a
-- caller reading this table sees exactly what the board said, not a
-- reinterpretation of it.

CREATE TABLE board_manifest_report (
    board_id    BIGINT PRIMARY KEY REFERENCES board(board_id) ON DELETE CASCADE,
    reported_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE board_manifest_report_entry (
    board_manifest_report_entry_id BIGSERIAL PRIMARY KEY,
    board_id       BIGINT NOT NULL REFERENCES board_manifest_report(board_id) ON DELETE CASCADE,
    -- Canonical hardware key components (FR18) -- see device_config_entry
    -- (migration 028) for the NULL-vs-0 i2c_address convention this
    -- mirrors exactly.
    i2c_address    SMALLINT,
    mux_path       JSONB NOT NULL DEFAULT '[]'::jsonb,
    sensor_type_id BIGINT NOT NULL REFERENCES sensor_type(sensor_type_id),
    -- Verbatim report fields (firmware.SensorDescriptor) -- never
    -- resolved/reinterpreted server-side.
    name           TEXT NOT NULL,
    unit           TEXT NOT NULL,
    chip_model     TEXT NOT NULL DEFAULT ''
);

-- FR49: "give me every entry in the last reported manifest for this
-- board" -- the whole-table replace-on-write above always keys off this.
CREATE INDEX idx_board_manifest_report_entry_board_id
    ON board_manifest_report_entry(board_id);

-- One entry per canonical hardware key within a single board's reported
-- manifest -- mirrors idx_device_config_entry_hw_key's own partial-unique
-- shape (migration 028) exactly, including leaving an absent (NULL)
-- i2c_address outside the index.
CREATE UNIQUE INDEX idx_board_manifest_report_entry_hw_key
    ON board_manifest_report_entry(board_id, i2c_address, sensor_type_id, (mux_path::text))
    WHERE i2c_address IS NOT NULL;
