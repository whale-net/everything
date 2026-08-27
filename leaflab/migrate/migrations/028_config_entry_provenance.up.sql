-- Migration 028: per-entry provenance on a stored device_config version (FR82.4, Phase 4)
--
-- 028 is the next free number verified against every plan/1166-v2-* and
-- plan/1166-rebuild-trunk worktree on disk at authoring time (highest seen:
-- 027, on rebuild-trunk); state in the PR since sibling v2 branches on
-- plan/1166 have collided on migration numbers before (see 016's own note).
--
-- device_config.config_json (migration 007) stores a whole pushed payload
-- as one opaque protojson blob -- it has no notion of "this one entry was
-- named by the caller" vs "this one entry was carried forward unchanged".
-- FR82.4 requires exactly that distinction, per entry, for every stored
-- config version: "authored" (named by the caller in the request that
-- stored it, including in a remove list) or "materialised" (carried
-- forward from an EDIT push's base). FR35.2 and FR37 return it per entry;
-- FR1.3's skip visibility is keyed on it.
--
-- A table, not a field inside config_json, per the Scaffold note: FR35.2
-- and FR37 return provenance per entry (a table is directly queryable by
-- key; a JSONB field would require unpacking config_json's sensors array
-- server-side on every read that needs it), and this table's key columns
-- double as the canonical hardware key (FR18: i2c_address, mux_path,
-- sensor_type) EDIT-scope materialisation and removal-key resolution
-- (leaflab/api/config) operate on -- the same key idx_sensor_hw_address
-- enforces uniqueness on and sensor_hw_history (migration 013) records
-- intervals under. Column names and the i2c_address NULL-means-absent
-- convention deliberately match sensor_hw_history's so
-- //leaflab/hwkey.Key.SQLPredicate works unmodified against either table
-- (see hwkey.Key's doc comment).
--
-- This table's rows are written once, when a device_config row is stored
-- (COMPLETE: every entry "authored"; EDIT: named entries "authored", the
-- rest of the materialised base "materialised") -- append-only, like
-- device_config itself; not SCD2 (NFR6.3 does not apply -- there is no
-- current/superseded distinction within one config version's entries).

CREATE TABLE device_config_entry (
    device_config_entry_id BIGSERIAL PRIMARY KEY,
    config_id      BIGINT NOT NULL REFERENCES device_config(config_id) ON DELETE CASCADE,
    -- Canonical hardware key components (FR18), matching sensor_hw_history's
    -- i2c_address/mux_path convention exactly: NULL means "no address
    -- recorded at all" (hwkey.AddressOpt Absent); 0 is the legacy
    -- manifests' distinct "unknown address" sentinel and a real, present
    -- value (FR18.2) -- never conflated with NULL. A payload entry always
    -- carries a real, present address (real or the 0 sentinel): proto3's
    -- i2c_address is a plain (non-optional) uint32, so NULL here is not
    -- reachable from a push today, but the column allows it rather than
    -- assume that always holds, matching sensor_hw_history's own shape.
    i2c_address    SMALLINT,
    mux_path       JSONB NOT NULL DEFAULT '[]'::jsonb,
    sensor_type_id BIGINT NOT NULL REFERENCES sensor_type(sensor_type_id),
    -- FR82.4's per-entry provenance. CHECK, not a separate lookup table:
    -- two fixed values, never queried by name elsewhere.
    provenance     TEXT NOT NULL CHECK (provenance IN ('authored', 'materialised'))
);

-- FR35.2/FR37: "give me every entry (and its provenance) for this stored
-- config version".
CREATE INDEX idx_device_config_entry_config_id ON device_config_entry(config_id);

-- One entry per canonical hardware key within a single stored config
-- version -- mirrors idx_sensor_hw_address's own partial-unique shape
-- (migration 009) exactly, including leaving an absent (NULL) i2c_address
-- outside the index: such an entry is unaddressable by any remove key
-- (FR82.4) and is not a candidate for this uniqueness check either.
CREATE UNIQUE INDEX idx_device_config_entry_hw_key
    ON device_config_entry(config_id, i2c_address, sensor_type_id, (mux_path::text))
    WHERE i2c_address IS NOT NULL;
