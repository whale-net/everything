package main

// Table-driven, pure-function coverage for ComposeDesiredSensors (FR8) --
// issue #1768's Testing criteria 1-7. No DB, no transport: these construct
// InventorySensor/SensorConfig values directly and assert on
// ComposeDesiredSensors' return value. Handler-level coverage (criteria 8,
// 9 -- PushDeviceConfig actually calls through to the composed list) lives
// in server_test.go alongside the rest of the PushDeviceConfig fixtures.

import (
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
)

// -- fixture builders ---------------------------------------------------

// noMux is a sensor directly on the root I2C bus -- an empty mux_path, per
// SensorConfig.mux_path's own doc comment ("empty = sensor directly on root
// bus"). Every fixture below uses it: hwKey only needs i2c_address to stay
// distinct across a test's fixtures, and a non-empty mux_path would just
// add noise to every assertion without exercising anything makeHWKey
// doesn't already cover via i2c_address alone.
var noMux []*configpb.MuxHop

func inv(i2cAddr uint32, name string, regionID *int64) InventorySensor {
	return InventorySensor{
		SensorID:   int64(i2cAddr),
		Name:       name,
		Unit:       "°C",
		SensorType: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE,
		MuxPath:    noMux,
		I2CAddress: i2cAddr,
		RegionID:   regionID,
	}
}

func cfg(i2cAddr uint32, name string) *configpb.SensorConfig {
	return &configpb.SensorConfig{
		MuxPath:    noMux,
		I2CAddress: i2cAddr,
		Name:       name,
		SensorType: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE,
	}
}

// nameOverride is a sparse override that sets only Name -- the shape a
// caller sends to rename one sensor without touching its other fields.
func nameOverride(i2cAddr uint32, name string) *configpb.SensorConfig {
	return &configpb.SensorConfig{
		MuxPath:    noMux,
		I2CAddress: i2cAddr,
		Name:       name,
	}
}

func marshalAll(t *testing.T, sensors []*configpb.SensorConfig) [][]byte {
	t.Helper()
	out := make([][]byte, len(sensors))
	for i, s := range sensors {
		b, err := proto.Marshal(s)
		require.NoError(t, err)
		out[i] = b
	}
	return out
}

// -- criterion 1 ----------------------------------------------------------

// TestComposeDesiredSensors_OverrideOneOfThree_OthersUnchanged is Testing
// criterion 1: a board with 3 sensors, caller overrides 1 -- output has 3
// entries, the 2 untouched ones byte-identical to their last-accepted
// state. This is FR8's core guarantee: renaming/reconfiguring one sensor
// must not remove or reset the board's other sensors (LB3).
func TestComposeDesiredSensors_OverrideOneOfThree_OthersUnchanged(t *testing.T) {
	lastAccepted := []*configpb.SensorConfig{
		cfg(0x10, "topsoil"),
		cfg(0x11, "canopy"),
		cfg(0x12, "root"),
	}
	overrides := []*configpb.SensorConfig{
		nameOverride(0x11, "canopy_renamed"),
	}

	got := ComposeDesiredSensors(nil, lastAccepted, overrides)

	require.Len(t, got, 3)
	byAddr := indexByI2CAddress(got)
	assert.True(t, proto.Equal(cfg(0x10, "topsoil"), byAddr[0x10]), "untouched sensor 0x10 must be byte-identical to its last-accepted state")
	assert.Equal(t, "canopy_renamed", byAddr[0x11].GetName(), "overridden sensor must carry the new name")
	assert.True(t, proto.Equal(cfg(0x12, "root"), byAddr[0x12]), "untouched sensor 0x12 must be byte-identical to its last-accepted state")
}

// -- criterion 2 ----------------------------------------------------------

// TestComposeDesiredSensors_EmptyOverrides_NoOp is Testing criterion 2: an
// empty override list is a no-op, not a wipe -- all 3 sensors survive
// unchanged.
func TestComposeDesiredSensors_EmptyOverrides_NoOp(t *testing.T) {
	lastAccepted := []*configpb.SensorConfig{
		cfg(0x10, "topsoil"),
		cfg(0x11, "canopy"),
		cfg(0x12, "root"),
	}

	got := ComposeDesiredSensors(nil, lastAccepted, nil)

	require.Len(t, got, 3)
	byAddr := indexByI2CAddress(got)
	assert.True(t, proto.Equal(cfg(0x10, "topsoil"), byAddr[0x10]))
	assert.True(t, proto.Equal(cfg(0x11, "canopy"), byAddr[0x11]))
	assert.True(t, proto.Equal(cfg(0x12, "root"), byAddr[0x12]))
}

// -- criterion 3 ----------------------------------------------------------

// TestComposeDesiredSensors_InInventoryOnly is Testing criterion 3: a
// sensor present in the DB inventory but absent from the last accepted
// config (e.g. registered after the last push) is still present in the
// output.
func TestComposeDesiredSensors_InInventoryOnly(t *testing.T) {
	inventory := []InventorySensor{inv(0x20, "new_sensor", nil)}

	got := ComposeDesiredSensors(inventory, nil, nil)

	require.Len(t, got, 1)
	assert.Equal(t, uint32(0x20), got[0].GetI2CAddress())
	assert.Equal(t, "new_sensor", got[0].GetName())
}

// -- criterion 4 ----------------------------------------------------------

// TestComposeDesiredSensors_InLastAcceptedOnly is Testing criterion 4: a
// sensor present in the last accepted config but absent from the current DB
// inventory (the DB has not caught up yet) is still carried through, not
// silently dropped.
func TestComposeDesiredSensors_InLastAcceptedOnly(t *testing.T) {
	lastAccepted := []*configpb.SensorConfig{cfg(0x30, "pending_sensor")}

	got := ComposeDesiredSensors(nil, lastAccepted, nil)

	require.Len(t, got, 1)
	assert.True(t, proto.Equal(cfg(0x30, "pending_sensor"), got[0]))
}

// -- criterion 5 ----------------------------------------------------------

// TestComposeDesiredSensors_OverrideMatchingNoKnownSensor_Added is Testing
// criterion 5: an override matching no known hardware identity (a sensor
// neither in inventory nor in the last accepted config) is added to the
// output -- that is how a new sensor is configured, not an error and not
// dropped.
func TestComposeDesiredSensors_OverrideMatchingNoKnownSensor_Added(t *testing.T) {
	overrides := []*configpb.SensorConfig{cfg(0x40, "brand_new")}

	got := ComposeDesiredSensors(nil, nil, overrides)

	require.Len(t, got, 1)
	assert.True(t, proto.Equal(cfg(0x40, "brand_new"), got[0]))
}

// -- criterion 6 ----------------------------------------------------------

// TestComposeDesiredSensors_MatchByHWIdentity_NotForkedByRename is Testing
// criterion 6: matching is by (mux_path, i2c_address), never by name. A
// sensor whose name changed between the last accepted config and the
// current DB inventory must be matched into one output entry, not forked
// into two.
func TestComposeDesiredSensors_MatchByHWIdentity_NotForkedByRename(t *testing.T) {
	inventory := []InventorySensor{inv(0x50, "renamed_in_db", nil)}
	lastAccepted := []*configpb.SensorConfig{cfg(0x50, "original_name")}

	got := ComposeDesiredSensors(inventory, lastAccepted, nil)

	require.Len(t, got, 1, "same hardware identity across inventory and last-accepted must match into one entry, not fork")
	assert.Equal(t, uint32(0x50), got[0].GetI2CAddress())
}

// -- criterion 7 ----------------------------------------------------------

// TestComposeDesiredSensors_Deterministic is Testing criterion 7:
// composing the same inputs twice produces byte-identical proto.Marshal
// output, and permuting the input lists' order produces the same output --
// this is what makes FR9's composition-parity assertion checkable.
func TestComposeDesiredSensors_Deterministic(t *testing.T) {
	inventory := []InventorySensor{
		inv(0x10, "a", nil),
		inv(0x11, "b", nil),
		inv(0x12, "c", nil),
	}
	lastAccepted := []*configpb.SensorConfig{
		cfg(0x11, "b_accepted"),
		cfg(0x13, "d_accepted"),
	}
	overrides := []*configpb.SensorConfig{
		nameOverride(0x12, "c_override"),
		cfg(0x40, "new"),
	}

	first := ComposeDesiredSensors(inventory, lastAccepted, overrides)
	second := ComposeDesiredSensors(inventory, lastAccepted, overrides)
	assert.Equal(t, marshalAll(t, first), marshalAll(t, second), "composing identical inputs twice must be byte-identical")

	// Permute the order of each input list; the composed, sorted output
	// must be unaffected.
	permutedInventory := []InventorySensor{inventory[2], inventory[0], inventory[1]}
	permutedLastAccepted := []*configpb.SensorConfig{lastAccepted[1], lastAccepted[0]}
	permutedOverrides := []*configpb.SensorConfig{overrides[1], overrides[0]}

	permuted := ComposeDesiredSensors(permutedInventory, permutedLastAccepted, permutedOverrides)
	assert.Equal(t, marshalAll(t, first), marshalAll(t, permuted), "permuting input order must not change the composed, sorted output")

	// Shuffle for good measure -- a fixed rng seed keeps this
	// deterministic across runs while still exercising a different
	// permutation than the hand-picked one above.
	rng := rand.New(rand.NewSource(42))
	shuffledInventory := append([]InventorySensor(nil), inventory...)
	rng.Shuffle(len(shuffledInventory), func(i, j int) {
		shuffledInventory[i], shuffledInventory[j] = shuffledInventory[j], shuffledInventory[i]
	})
	shuffled := ComposeDesiredSensors(shuffledInventory, lastAccepted, overrides)
	assert.Equal(t, marshalAll(t, first), marshalAll(t, shuffled))
}

// indexByI2CAddress indexes a composed sensor list by i2c_address for
// assertions that care about one specific sensor's fields rather than
// output order (order is covered separately by the determinism test).
func indexByI2CAddress(sensors []*configpb.SensorConfig) map[uint32]*configpb.SensorConfig {
	out := make(map[uint32]*configpb.SensorConfig, len(sensors))
	for _, s := range sensors {
		out[s.GetI2CAddress()] = s
	}
	return out
}
