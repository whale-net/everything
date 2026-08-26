package canonkey

import (
	"fmt"
	"strconv"
	"strings"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	firmwarepb "github.com/whale-net/everything/firmware/proto"
)

// MuxHop represents one step in a cascaded I2C mux chain.
// Ordered outer → inner: mux_path[0] is the first mux from the root bus;
// mux_path[len-1] is the mux the sensor hangs off directly.
type MuxHop struct {
	MuxAddress uint32
	MuxChannel uint32
}

// Key represents the canonical hardware key: (i2c_address, mux_path, sensor_type).
// All three components have exactly one canonical encoding at the proto/JSON boundary,
// so two semantically equal keys can never compare unequal at any layer.
type Key struct {
	I2CAddress uint32
	MuxPath    []MuxHop
	SensorType int32 // firmware.SensorType enum value
}

// ChipKey represents the canonical hardware key without sensor_type:
// (i2c_address, mux_path). Used by FR82.4 for removing entries by chip address.
type ChipKey struct {
	I2CAddress uint32
	MuxPath    []MuxHop
}

// CanonicalizeKey takes a SensorConfig and returns the canonical Key.
// Canonicalization occurs at the proto/JSON boundary before validation
// and before any comparison or storage:
//
// - mux_path: absent and explicit 0 collapse to one form, no fractional parts emitted
// - i2c_address: accepts hex and decimal spellings, stores one canonical form
//   (absent and 0 remain distinguishable; 0 is the legacy "unknown" sentinel)
// - sensor_type: must be the stable type identifier (enum), never a display string
func CanonicalizeKey(cfg *configpb.SensorConfig) (*Key, error) {
	if cfg == nil {
		return nil, fmt.Errorf("SensorConfig is nil")
	}

	// Canonicalize i2c_address: must be present and non-zero
	// (absence and 0 remain distinguishable; 0 means unknown)
	i2cAddr := cfg.I2CAddress

	// Canonicalize mux_path: absent and explicit 0 resolve to one form (empty slice)
	// No fractional parts are emitted (only integer values)
	muxPath := canonicalizeMuxPath(cfg.MuxPath)

	// Canonicalize sensor_type: must be the enum value, not a display string
	sensorType := int32(cfg.SensorType)

	return &Key{
		I2CAddress: i2cAddr,
		MuxPath:    muxPath,
		SensorType: sensorType,
	}, nil
}

// canonicalizeMuxPath normalizes the mux path to a canonical form.
// Empty or nil input returns an empty slice (representing direct on root bus).
// No fractional parts are allowed.
func canonicalizeMuxPath(muxPath []*configpb.MuxHop) []MuxHop {
	if len(muxPath) == 0 {
		return []MuxHop{}
	}

	canonical := make([]MuxHop, 0, len(muxPath))
	for _, hop := range muxPath {
		if hop != nil {
			canonical = append(canonical, MuxHop{
				MuxAddress: hop.MuxAddress,
				MuxChannel: hop.MuxChannel,
			})
		}
	}

	if len(canonical) == 0 {
		return []MuxHop{}
	}

	return canonical
}

// CanonicalizeChipKey takes a SensorConfig and returns the canonical ChipKey
// (i2c_address and mux_path, but not sensor_type).
func CanonicalizeChipKey(cfg *configpb.SensorConfig) (*ChipKey, error) {
	if cfg == nil {
		return nil, fmt.Errorf("SensorConfig is nil")
	}

	i2cAddr := cfg.I2CAddress
	muxPath := canonicalizeMuxPath(cfg.MuxPath)

	return &ChipKey{
		I2CAddress: i2cAddr,
		MuxPath:    muxPath,
	}, nil
}

// Equals compares two Keys for semantic equality.
func (k *Key) Equals(other *Key) bool {
	if k == nil && other == nil {
		return true
	}
	if k == nil || other == nil {
		return false
	}

	if k.I2CAddress != other.I2CAddress {
		return false
	}
	if k.SensorType != other.SensorType {
		return false
	}
	return muxPathEquals(k.MuxPath, other.MuxPath)
}

// String returns a human-readable representation of the Key.
func (k *Key) String() string {
	if k == nil {
		return "<nil>"
	}
	return fmt.Sprintf("Key{i2c:0x%02x, mux:%v, type:%d}", k.I2CAddress, k.MuxPath, k.SensorType)
}

// Equals compares two ChipKeys for semantic equality.
func (ck *ChipKey) Equals(other *ChipKey) bool {
	if ck == nil && other == nil {
		return true
	}
	if ck == nil || other == nil {
		return false
	}

	if ck.I2CAddress != other.I2CAddress {
		return false
	}
	return muxPathEquals(ck.MuxPath, other.MuxPath)
}

// String returns a human-readable representation of the ChipKey.
func (ck *ChipKey) String() string {
	if ck == nil {
		return "<nil>"
	}
	return fmt.Sprintf("ChipKey{i2c:0x%02x, mux:%v}", ck.I2CAddress, ck.MuxPath)
}

// muxPathEquals compares two mux paths for equality.
func muxPathEquals(a, b []MuxHop) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].MuxAddress != b[i].MuxAddress || a[i].MuxChannel != b[i].MuxChannel {
			return false
		}
	}
	return true
}

// ParseI2CAddress parses an i2c_address from string, accepting both hex and decimal.
// Returns the canonical uint32 representation.
// Examples: "0x1A", "0x1a", "26" all parse to 26.
func ParseI2CAddress(s string) (uint32, error) {
	s = strings.TrimSpace(s)

	// Try hex format
	if strings.HasPrefix(strings.ToLower(s), "0x") {
		var addr uint64
		_, err := fmt.Sscanf(s, "0x%x", &addr)
		if err == nil && addr <= 0xFF {
			return uint32(addr), nil
		}
	}

	// Try decimal format
	addr, err := strconv.ParseUint(s, 10, 32)
	if err == nil && addr <= 0xFF {
		return uint32(addr), nil
	}

	return 0, fmt.Errorf("invalid i2c_address %q: must be hex (0x00-0xFF) or decimal (0-255)", s)
}

// RenderI2CAddress renders an i2c_address as a canonical hex string (0x00-0xFF).
func RenderI2CAddress(addr uint32) string {
	return fmt.Sprintf("0x%02x", addr)
}

// RenderSensorType renders a sensor type enum value as its display name.
func RenderSensorType(t int32) string {
	sensorType := firmwarepb.SensorType(t)
	return strings.TrimPrefix(sensorType.String(), "SENSOR_TYPE_")
}

// ValidateSensorType validates that a sensor type is a known enum value.
// Returns error if the string is not a valid enum name.
func ValidateSensorType(name string) (int32, error) {
	// Try to parse as enum name
	enumName := "SENSOR_TYPE_" + strings.ToUpper(name)
	val, ok := firmwarepb.SensorType_value[enumName]
	if !ok {
		return 0, fmt.Errorf("unknown sensor type %q", name)
	}
	return val, nil
}

// ValidateAndCanonicalizeSensorConfig validates that a SensorConfig has valid
// fields and canonicalizes the mux_path. This should be called on ingress at 
// the proto/JSON boundary, before validation and storage.
// Returns an error if sensor_type is not a valid enum value.
func ValidateAndCanonicalizeSensorConfig(cfg *configpb.SensorConfig) error {
	if cfg == nil {
		return fmt.Errorf("SensorConfig is nil")
	}

	// Ensure sensor_type is a valid enum value (protobuf deserialization already 
	// validates this, but we check to be explicit)
	if _, ok := firmwarepb.SensorType_name[int32(cfg.SensorType)]; !ok {
		return fmt.Errorf("invalid sensor_type value: %d", cfg.SensorType)
	}

	// Canonicalize mux_path: ensure it's not nil, replace with empty slice if nil
	if cfg.MuxPath == nil {
		cfg.MuxPath = []*configpb.MuxHop{}
	}

	return nil
}
