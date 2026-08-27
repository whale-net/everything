// Package hwkey implements FR18: one canonical encoding for the whole
// hardware key.
//
// The canonical hardware key is (i2c_address, mux_path, sensor_type). All
// three components have exactly one canonical encoding at the proto/JSON
// boundary, so two semantically equal keys never compare unequal at any
// layer, in any surface, at rest or in flight.
//
//   - MuxPath (leaflab/DATA.md "mux_path JSONB Format"): an absent key and
//     an explicit 0 resolve to one form, and integer-valued fields are
//     never emitted with a fractional part.
//   - AddressOpt: one canonical representation and one comparison rule for
//     i2c_address, so 0x1A, 0x1a and 26 are the same key. Absent and 0 are
//     not interchangeable -- 0 is the legacy manifests' "unknown address"
//     sentinel and is what makes an entry unaddressable under FR82.4.
//   - SensorTypeID: the catalog's stable type identifier, never a display
//     string or a locale-dependent label.
//
// This package is used by //leaflab/api and //leaflab/processor, and
// (Phase 4) the config path. FR16.1 records history intervals under Key;
// FR82.3/FR82.4 remove config entries by ChipKey; idx_sensor_hw_address
// already enforces uniqueness on Key's SQL-facing columns.
package hwkey
