package hwkey

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// AddressOpt is a nullable I2C address that distinguishes three states,
// per FR18.2: absent (no address recorded at all), an explicit 0 (the
// legacy manifests' "unknown address" sentinel -- an entry in this state
// is unaddressable under FR82.4), and a real, present address. The zero
// value of AddressOpt is Absent.
type AddressOpt struct {
	present bool
	value   uint16
}

// Absent is the zero-value AddressOpt: no address recorded at all.
var Absent = AddressOpt{}

// Address constructs a present AddressOpt, including the value 0 (the
// "unknown address" sentinel, distinct from Absent).
func Address(v uint16) AddressOpt {
	return AddressOpt{present: true, value: v}
}

// IsAbsent reports whether no address was recorded at all.
func (a AddressOpt) IsAbsent() bool { return !a.present }

// IsUnknownSentinel reports whether this is the legacy manifests'
// "unknown address" (an explicit 0), which is distinct from Absent per
// FR18.2.
func (a AddressOpt) IsUnknownSentinel() bool { return a.present && a.value == 0 }

// Value returns the address and whether one was present at all. ok==false
// (Absent) must never be treated the same as a present 0.
func (a AddressOpt) Value() (v uint16, ok bool) { return a.value, a.present }

// Equal compares two AddressOpt values semantically: Absent never equals
// a present value (including a present 0), and two present values are
// equal iff their numeric value matches.
func (a AddressOpt) Equal(other AddressOpt) bool {
	if a.present != other.present {
		return false
	}
	return !a.present || a.value == other.value
}

// String renders the one canonical form for this address: "absent" for
// Absent, otherwise decimal (matching the sensor.i2c_address SMALLINT
// column's own representation).
func (a AddressOpt) String() string {
	if !a.present {
		return "absent"
	}
	return strconv.FormatUint(uint64(a.value), 10)
}

// ParseAddress parses a human- or manifest-supplied address string into a
// present AddressOpt. It accepts hex with a case-insensitive "0x" prefix
// (0x1A, 0x1a) and plain decimal (26) -- 0x1A, 0x1a and 26 all parse to
// the same value (FR18.2). An empty string is rejected; callers that need
// to represent "no address supplied" use Absent directly, not
// ParseAddress.
func ParseAddress(s string) (AddressOpt, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return AddressOpt{}, fmt.Errorf("hwkey: empty i2c address")
	}
	base := 10
	digits := trimmed
	if strings.HasPrefix(strings.ToLower(trimmed), "0x") {
		base = 16
		digits = trimmed[2:]
	}
	v, err := strconv.ParseUint(digits, base, 16)
	if err != nil {
		return AddressOpt{}, fmt.Errorf("hwkey: invalid i2c address %q: %w", s, err)
	}
	return Address(uint16(v)), nil
}

// AddressFromPtr converts the *uint32 idiom used for an `optional uint32`
// proto3 field into an AddressOpt: nil is Absent, otherwise present.
func AddressFromPtr(v *uint32) AddressOpt {
	if v == nil {
		return AddressOpt{}
	}
	return Address(uint16(*v))
}

// Ptr renders this AddressOpt back into the *uint32 idiom for an
// `optional uint32` proto3 field: nil for Absent, otherwise a pointer to
// the value.
func (a AddressOpt) Ptr() *uint32 {
	if !a.present {
		return nil
	}
	v := uint32(a.value)
	return &v
}

// MarshalJSON is the one canonical proto/JSON-boundary encoding for
// AddressOpt: Absent marshals to JSON null, a present value (including 0)
// marshals to a JSON number.
func (a AddressOpt) MarshalJSON() ([]byte, error) {
	if !a.present {
		return []byte("null"), nil
	}
	return json.Marshal(a.value)
}

// UnmarshalJSON is MarshalJSON's inverse: JSON null decodes to Absent, a
// JSON number decodes to a present value.
func (a *AddressOpt) UnmarshalJSON(data []byte) error {
	if strings.TrimSpace(string(data)) == "null" {
		*a = AddressOpt{}
		return nil
	}
	var v uint16
	if err := json.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("hwkey: invalid i2c_address: %w", err)
	}
	*a = Address(v)
	return nil
}
