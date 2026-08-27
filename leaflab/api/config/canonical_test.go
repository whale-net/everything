package config

import (
	"testing"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/hwkey"
)

// TestCanonicalKey_AssemblesThreeComponents proves CanonicalKey builds a
// hwkey.Key from exactly sc's i2c_address/mux_path and the caller-supplied
// sensorTypeID -- the FR18 canonical hardware key FR82 keys everything
// (materialisation, removal-key resolution, provenance storage) on.
func TestCanonicalKey_AssemblesThreeComponents(t *testing.T) {
	sc := &configpb.SensorConfig{
		I2CAddress: 0x44,
		MuxPath: []*configpb.MuxHop{
			{MuxAddress: 0x70, MuxChannel: 3},
		},
	}
	got := CanonicalKey(sc, hwkey.SensorTypeID(7))

	want := hwkey.Key{
		I2CAddress:   hwkey.Address(0x44),
		MuxPath:      hwkey.MuxPath{{MuxAddress: 0x70, MuxChannel: 3}},
		SensorTypeID: hwkey.SensorTypeID(7),
	}
	if !got.Equal(want) {
		t.Errorf("CanonicalKey = %v, want %v", got, want)
	}
}

// TestCanonicalKey_ZeroI2CAddress_IsUnknownSentinelNotAbsent proves
// CanonicalKey preserves FR18.2's "unknown address" sentinel (an explicit
// 0) rather than folding it into Absent -- FR82.4 depends on being able to
// tell the two apart (an entry with no addressable i2c_address has no
// removal path via EDIT).
func TestCanonicalKey_ZeroI2CAddress_IsUnknownSentinelNotAbsent(t *testing.T) {
	sc := &configpb.SensorConfig{I2CAddress: 0}
	got := CanonicalKey(sc, hwkey.SensorTypeID(1))

	if got.I2CAddress.IsAbsent() {
		t.Fatal("CanonicalKey with i2c_address=0 produced Absent, want the present 'unknown' sentinel")
	}
	if !got.I2CAddress.IsUnknownSentinel() {
		t.Error("CanonicalKey with i2c_address=0 did not report IsUnknownSentinel")
	}
}

// TestCanonicalKey_EmptyMuxPath_IsRootBus proves an entry with no mux hops
// canonicalises to an empty (not nil-vs-empty-ambiguous) MuxPath -- the
// "directly on root bus" case every chip-key/full-key comparison in
// removekey.go relies on comparing consistently.
func TestCanonicalKey_EmptyMuxPath_IsRootBus(t *testing.T) {
	sc := &configpb.SensorConfig{I2CAddress: 0x23}
	got := CanonicalKey(sc, hwkey.SensorTypeID(1))
	if len(got.MuxPath) != 0 {
		t.Errorf("MuxPath = %v, want empty (root bus)", got.MuxPath)
	}
}
