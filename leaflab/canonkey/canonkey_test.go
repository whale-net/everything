package canonkey

import (
	"testing"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	firmwarepb "github.com/whale-net/everything/firmware/proto"
)

// TestCanonicalizeKey_Basic verifies that CanonicalizeKey returns a Key with correct fields.
func TestCanonicalizeKey_Basic(t *testing.T) {
	cfg := &configpb.SensorConfig{
		I2CAddress: 26,
		MuxPath: []*configpb.MuxHop{
			{MuxAddress: 112, MuxChannel: 5},
		},
		SensorType: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE,
	}

	key, err := CanonicalizeKey(cfg)
	if err != nil {
		t.Fatalf("CanonicalizeKey failed: %v", err)
	}

	if key.I2CAddress != 26 {
		t.Errorf("expected I2CAddress 26, got %d", key.I2CAddress)
	}
	if key.SensorType != int32(firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE) {
		t.Errorf("expected SensorType TEMPERATURE, got %d", key.SensorType)
	}
	if len(key.MuxPath) != 1 {
		t.Errorf("expected MuxPath length 1, got %d", len(key.MuxPath))
	}
}

// TestCanonicalizeKey_NilInput returns error for nil SensorConfig.
func TestCanonicalizeKey_NilInput(t *testing.T) {
	_, err := CanonicalizeKey(nil)
	if err == nil {
		t.Fatal("expected error for nil SensorConfig")
	}
}

// TestCanonicalizeChipKey_Basic verifies that CanonicalizeChipKey returns a ChipKey with correct fields.
func TestCanonicalizeChipKey_Basic(t *testing.T) {
	cfg := &configpb.SensorConfig{
		I2CAddress: 26,
		MuxPath: []*configpb.MuxHop{
			{MuxAddress: 112, MuxChannel: 5},
		},
	}

	chipKey, err := CanonicalizeChipKey(cfg)
	if err != nil {
		t.Fatalf("CanonicalizeChipKey failed: %v", err)
	}

	if chipKey.I2CAddress != 26 {
		t.Errorf("expected I2CAddress 26, got %d", chipKey.I2CAddress)
	}
	if len(chipKey.MuxPath) != 1 {
		t.Errorf("expected MuxPath length 1, got %d", len(chipKey.MuxPath))
	}
}

// TestParseI2CAddress_Hex tests parsing hex format addresses.
func TestParseI2CAddress_Hex(t *testing.T) {
	tests := []struct {
		input    string
		expected uint32
	}{
		{"0x1A", 26},
		{"0x1a", 26},
		{"0x00", 0},
		{"0xFF", 255},
	}

	for _, tt := range tests {
		got, err := ParseI2CAddress(tt.input)
		if err != nil {
			t.Errorf("ParseI2CAddress(%q) failed: %v", tt.input, err)
		}
		if got != tt.expected {
			t.Errorf("ParseI2CAddress(%q) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

// TestParseI2CAddress_Decimal tests parsing decimal format addresses.
func TestParseI2CAddress_Decimal(t *testing.T) {
	tests := []struct {
		input    string
		expected uint32
	}{
		{"26", 26},
		{"0", 0},
		{"255", 255},
	}

	for _, tt := range tests {
		got, err := ParseI2CAddress(tt.input)
		if err != nil {
			t.Errorf("ParseI2CAddress(%q) failed: %v", tt.input, err)
		}
		if got != tt.expected {
			t.Errorf("ParseI2CAddress(%q) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}
