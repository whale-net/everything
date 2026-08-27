package config

import (
	"fmt"
	"strings"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	"github.com/whale-net/everything/leaflab/hwkey"
)

// FailureClass classifies one FR39 validation failure. Mirrored onto
// leaflab/api/contract.FailureClass by the caller (this package stays
// contract-free, matching materialise.go/removekey.go's existing split,
// per doc.go) -- these values are this package's own vocabulary, not
// contract's.
type FailureClass string

const (
	// FailureInvalidI2CAddress: an entry's i2c_address falls outside the
	// bus's usable 7-bit range (MinI2CAddress-MaxI2CAddress).
	FailureInvalidI2CAddress FailureClass = "invalid_i2c_address"
	// FailureChipTypeNotProduced: an entry names a (chip_type, sensor_type)
	// pair the catalog does not produce.
	FailureChipTypeNotProduced FailureClass = "chip_measurement_type_not_produced"
	// FailureInvalidPollInterval: an entry's poll_interval_ms falls outside
	// the stated min/max (leaflab/api/ENV.md).
	FailureInvalidPollInterval FailureClass = "invalid_poll_interval_ms"
	// FailureHardwareKeyCollision: two entries in the same payload
	// canonicalise to the same hardware key (FR18).
	FailureHardwareKeyCollision FailureClass = "hardware_key_collision"
	// FailureUnknownRemoveKey: a remove key matches no entry in the
	// materialisation base -- FR82.4/FR39's anti-silence rule: never a
	// silent no-op.
	FailureUnknownRemoveKey FailureClass = "unknown_remove_key"
	// FailureUnaddressableRemove: a remove key names the unknown-address
	// sentinel (FR18.2) -- FR82.4's stated EDIT limitation, with its named
	// failure class and remedy (push scope=COMPLETE with the entry
	// omitted).
	FailureUnaddressableRemove FailureClass = "unaddressable_remove"
)

// Failure is one FR39 validation failure, always naming the specific
// entry (EntryIndex, into whichever list Validate was given it in --
// adds for an add/change failure, removes for a removal failure) and
// Field it concerns (FR59) -- never just a payload-wide message that
// leaves the caller to guess which entry was wrong.
type Failure struct {
	EntryIndex int
	Field      string
	Class      FailureClass
	Message    string
}

// InvalidI2CAddressFailure builds a FailureInvalidI2CAddress failure
// against adds[entryIndex].i2c_address.
func InvalidI2CAddressFailure(entryIndex int, message string) Failure {
	return Failure{EntryIndex: entryIndex, Field: "i2c_address", Class: FailureInvalidI2CAddress, Message: message}
}

// ChipTypeNotProducedFailure builds a FailureChipTypeNotProduced failure
// against adds[entryIndex].sensor_type.
func ChipTypeNotProducedFailure(entryIndex int, message string) Failure {
	return Failure{EntryIndex: entryIndex, Field: "sensor_type", Class: FailureChipTypeNotProduced, Message: message}
}

// InvalidPollIntervalFailure builds a FailureInvalidPollInterval failure
// against adds[entryIndex].poll_interval_ms.
func InvalidPollIntervalFailure(entryIndex int, message string) Failure {
	return Failure{EntryIndex: entryIndex, Field: "poll_interval_ms", Class: FailureInvalidPollInterval, Message: message}
}

// HardwareKeyCollisionFailure builds a FailureHardwareKeyCollision
// failure against adds[entryIndex] as a whole -- the collision is between
// two entries, not one field of one entry.
func HardwareKeyCollisionFailure(entryIndex int, message string) Failure {
	return Failure{EntryIndex: entryIndex, Class: FailureHardwareKeyCollision, Message: message}
}

// UnknownRemoveKeyFailure builds a FailureUnknownRemoveKey failure
// against removes[entryIndex].
func UnknownRemoveKeyFailure(entryIndex int, message string) Failure {
	return Failure{EntryIndex: entryIndex, Class: FailureUnknownRemoveKey, Message: message}
}

// UnaddressableRemoveFailure builds a FailureUnaddressableRemove failure
// against removes[entryIndex].
func UnaddressableRemoveFailure(entryIndex int, message string) Failure {
	return Failure{EntryIndex: entryIndex, Class: FailureUnaddressableRemove, Message: message}
}

// Validation is FR39's result: every failure Validate found in one
// payload, together -- collected, never short-circuited on the first, so
// a single failure never masks the rest.
type Validation struct {
	Failures []Failure
}

// OK reports whether the payload validated cleanly.
func (v Validation) OK() bool { return len(v.Failures) == 0 }

func (v *Validation) add(f Failure) { v.Failures = append(v.Failures, f) }

// PollIntervalBounds is FR39's stated poll_interval_ms range -- see
// leaflab/api/ENV.md for the environment variables that set it. Both
// fields are meaningless at their zero value (every nonzero
// poll_interval_ms would fail MaxMs=0): a caller always supplies real
// bounds resolved from configuration, never the zero value, matching
// Catalog's "never silently disabled" posture above.
type PollIntervalBounds struct {
	MinMs uint32
	MaxMs uint32
}

// Catalog is FR39's resolved snapshot of which (chip, sensor_type) pairs
// the chip catalog (sensor_chip/sensor_chip_type/sensor_type, migrations
// 008/010) actually produces. Loading it from the DB is the caller's job
// (leaflab/api/repository.go) -- this package never queries the catalog
// itself, matching CanonicalKey's split (see canonical.go). The zero
// value is an empty, usable Catalog that produces nothing -- never
// treated as "skip this check", so a caller that forgets to populate it
// gets loud validation failures, not a silently-disabled one.
type Catalog struct {
	produces map[string]map[hwkey.SensorTypeID]bool
}

// NewCatalog returns an empty Catalog ready for Add calls.
func NewCatalog() *Catalog {
	return &Catalog{produces: make(map[string]map[hwkey.SensorTypeID]bool)}
}

// Add records that chipName's catalog entry produces sensorTypeID.
// chipName is matched case-insensitively against ChipTypeName's output
// (see that function's doc comment) -- callers pass chips.yaml's own
// sensor_chip.name spelling (e.g. "SHT3x") directly; Add upper-cases it.
func (c *Catalog) Add(chipName string, sensorTypeID hwkey.SensorTypeID) {
	key := strings.ToUpper(chipName)
	if c.produces[key] == nil {
		c.produces[key] = make(map[hwkey.SensorTypeID]bool)
	}
	c.produces[key][sensorTypeID] = true
}

// Produces reports whether chipName's catalog entry produces
// sensorTypeID. A nil Catalog produces nothing (never panics), matching
// the zero-value contract above.
func (c *Catalog) Produces(chipName string, sensorTypeID hwkey.SensorTypeID) bool {
	if c == nil {
		return false
	}
	return c.produces[strings.ToUpper(chipName)][sensorTypeID]
}

// Usable 7-bit I2C address range: 0x00-0x07 and 0x78-0x7F are reserved by
// the I2C specification and no real device ever answers there. 0 is
// additionally this codebase's "unknown address" sentinel (FR18.2,
// hwkey.AddressOpt) -- Validate exempts it below rather than merely
// letting it fall inside a widened range, since it is never actually
// treated as a bus address at all.
const (
	MinI2CAddress = 0x08
	MaxI2CAddress = 0x77
)

// Validate runs FR39's server-side checks against one push payload --
// everything the firmware would reject anyway, so a caller finds out
// before publishing to the device rather than from a rejected
// DeviceConfigAck.
//
// adds is the caller's authored add/change list -- under both scopes,
// this is always the payload's own sensors list (never a materialised
// carry-forward entry: those already passed this check when they were
// themselves accepted). removes is the EDIT-scope removal list, always
// empty under scope=COMPLETE. base is the board's current accepted
// config entries, consulted only to check whether a remove key matches
// anything -- nil is fine when removes is empty. catalog and bounds are
// resolved once by the caller and passed in, matching CanonicalKey's
// split: this package never talks to the DB (doc.go).
//
// Every failure found is collected into the returned Validation, never
// just the first -- a single failure must not mask the rest (FR39).
//
// Boundary with FR16.4 (see doc.go and the Phase 3 identity task): the
// within-payload collision check below catches two entries that
// canonicalise to the same hardware key. It does not, and cannot, catch a
// swap (sensor A's entry taking on sensor B's old address and vice
// versa) -- a swap produces two distinct canonical keys, not a
// collision. Swap handling belongs to identity resolution, not here.
func Validate(adds []Entry, removes []RemoveKey, base []Entry, catalog *Catalog, bounds PollIntervalBounds) Validation {
	var v Validation

	firstByKey := make(map[string]int, len(adds))
	for i, e := range adds {
		sc := e.Sensor

		if addr := sc.GetI2CAddress(); addr != 0 && (addr < MinI2CAddress || addr > MaxI2CAddress) {
			v.add(InvalidI2CAddressFailure(i, fmt.Sprintf(
				"I2C address %d is outside the valid range (%d-%d).", addr, MinI2CAddress, MaxI2CAddress)))
		}

		if chipName := ChipTypeName(sc.GetChipType()); chipName != "" &&
			sc.GetSensorType() != firmwarepb.SensorType_SENSOR_TYPE_UNKNOWN &&
			!catalog.Produces(chipName, e.Key.SensorTypeID) {
			v.add(ChipTypeNotProducedFailure(i, fmt.Sprintf(
				"%s does not produce this measurement type.", chipName)))
		}

		if ms := sc.GetPollIntervalMs(); ms != 0 && (ms < bounds.MinMs || ms > bounds.MaxMs) {
			v.add(InvalidPollIntervalFailure(i, fmt.Sprintf(
				"poll_interval_ms %d is outside the valid range (%d-%d).", ms, bounds.MinMs, bounds.MaxMs)))
		}

		key := e.Key.String()
		if first, collides := firstByKey[key]; collides {
			v.add(HardwareKeyCollisionFailure(i, fmt.Sprintf(
				"This entry's I2C address, mux path and sensor type are the same as sensors[%d]; both would resolve to the same hardware key.", first)))
		} else {
			firstByKey[key] = i
		}
	}

	for i, rk := range removes {
		if rk.Chip.I2CAddress.IsUnknownSentinel() {
			// FR82.4's named failure class and remedy -- checked here as a
			// static property of the remove key itself (Match makes the
			// identical check against base, but this needs no base to
			// know), so it's found even when base is nil/empty.
			v.add(UnaddressableRemoveFailure(i,
				"This entry has no I2C address on record and cannot be removed by an edit push; push scope=COMPLETE with it omitted from sensors instead."))
			continue
		}
		matched, _ := rk.Match(base) // err is always nil here: the one error Match returns is the sentinel case, already handled above.
		if len(matched) == 0 {
			v.add(UnknownRemoveKeyFailure(i,
				"This remove does not match any entry in the board's current accepted config."))
		}
	}

	return v
}
