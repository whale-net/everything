package hwkey

import "fmt"

// SensorTypeID is the sensor_type catalog's stable identifier (FR18.3) --
// never a display string or a locale-dependent label. Resolving a display
// string (e.g. "temperature") to a SensorTypeID is the catalog lookup's
// job, not this package's: no function in this package accepts one.
type SensorTypeID int64

// ChipKey identifies a physical chip's location on the I2C bus, without
// the SensorTypeID discriminator that distinguishes the chip's individual
// virtual sensors. One SHT3x chip is two sensor rows -- temperature and
// humidity -- that share one ChipKey but differ in SensorTypeID.
// FR82.4 (Phase 4) removes config entries keyed by ChipKey.
type ChipKey struct {
	I2CAddress AddressOpt
	MuxPath    MuxPath
}

// Equal compares two ChipKey values semantically.
func (c ChipKey) Equal(other ChipKey) bool {
	return c.I2CAddress.Equal(other.I2CAddress) && c.MuxPath.Equal(other.MuxPath)
}

// String is ChipKey's stable, canonical text form for logs and error
// details.
func (c ChipKey) String() string {
	return fmt.Sprintf("i2c=%s mux=%s", c.I2CAddress, c.MuxPath)
}

// Key is the canonical hardware key (FR18): (i2c_address, mux_path,
// sensor_type). Two semantically equal keys never compare unequal, at any
// layer, in any surface, at rest or in flight -- see AddressOpt, MuxPath
// and SensorTypeID for each component's one canonical encoding.
type Key struct {
	I2CAddress   AddressOpt
	MuxPath      MuxPath
	SensorTypeID SensorTypeID
}

// Chip returns the ChipKey portion of this Key, dropping the
// SensorTypeID discriminator.
func (k Key) Chip() ChipKey {
	return ChipKey{I2CAddress: k.I2CAddress, MuxPath: k.MuxPath}
}

// Equal compares two Key values semantically across all three
// components.
func (k Key) Equal(other Key) bool {
	return k.I2CAddress.Equal(other.I2CAddress) &&
		k.MuxPath.Equal(other.MuxPath) &&
		k.SensorTypeID == other.SensorTypeID
}

// String is Key's stable, canonical text form for logs and error
// details.
func (k Key) String() string {
	return fmt.Sprintf("i2c=%s mux=%s sensor_type_id=%d", k.I2CAddress, k.MuxPath, k.SensorTypeID)
}

// SQLPredicate returns a WHERE-clause fragment and its positional
// argument values matching this Key against idx_sensor_hw_address's
// (i2c_address, sensor_type_id, (mux_path::text)) columns, using
// $-numbered placeholders starting at argOffset+1 (pgx convention). The
// caller supplies the leading board_id predicate and argument separately
// -- Key does not carry a board_id.
//
// mux_path is compared via its canonical SQLText, not
// `mux_path = $::jsonb`, to match the index's own `(mux_path::text)`
// expression exactly: jsonb value equality and jsonb::text equality are
// not the same thing, since Postgres's jsonb type does not normalise a
// numeral's fractional formatting or backfill an omitted key to its zero
// value when computing ::text. Both sides of this comparison must already
// be in this package's canonical form for it to line up -- see MuxPath's
// doc comment.
func (k Key) SQLPredicate(argOffset int) (string, []any) {
	if k.I2CAddress.IsAbsent() {
		return fmt.Sprintf(
			"i2c_address IS NULL AND sensor_type_id = $%d AND mux_path::text = $%d",
			argOffset+1, argOffset+2,
		), []any{int64(k.SensorTypeID), k.MuxPath.SQLText()}
	}
	addr, _ := k.I2CAddress.Value()
	return fmt.Sprintf(
		"i2c_address = $%d AND sensor_type_id = $%d AND mux_path::text = $%d",
		argOffset+1, argOffset+2, argOffset+3,
	), []any{int32(addr), int64(k.SensorTypeID), k.MuxPath.SQLText()}
}
