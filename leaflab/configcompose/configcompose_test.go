package configcompose

// Table-driven, pure-function coverage for ComposeDesiredSensors (FR8) --
// issue #1768's Testing criteria 1-7. No DB, no transport: these construct
// InventorySensor/SensorConfig values directly and assert on
// ComposeDesiredSensors' return value. Handler-level coverage (criteria 8,
// 9 -- PushDeviceConfig actually calls through to the composed list) lives
// in leaflab/api/server_test.go alongside the rest of the PushDeviceConfig
// fixtures.

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

// -- co-located sensors (#1819) --------------------------------------------
//
// A chip like the SHT3x exposes two virtual sensors -- temperature and
// humidity -- at the same (i2c_address, mux_path), differentiated only by
// sensor_type. Every fixture below shares one hardware address (0x60)
// between a temperature and a humidity InventorySensor to exercise that.

const coLocatedAddr = 0x60

func invAt(i2cAddr uint32, sensorType firmwarepb.SensorType, sensorID int64, name string) InventorySensor {
	return InventorySensor{
		SensorID:   sensorID,
		Name:       name,
		Unit:       "°C",
		SensorType: sensorType,
		MuxPath:    noMux,
		I2CAddress: i2cAddr,
		RegionID:   nil,
	}
}

// coLocatedInventory is the shared fixture for all tests below: a
// temperature and a humidity sensor co-located at coLocatedAddr, with
// distinct sensor_ids.
func coLocatedInventory() []InventorySensor {
	return []InventorySensor{
		invAt(coLocatedAddr, firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE, 101, "sht3x_temp"),
		invAt(coLocatedAddr, firmwarepb.SensorType_SENSOR_TYPE_HUMIDITY, 102, "sht3x_humidity"),
	}
}

// typedOverride is a sparse override that sets Name and, when non-zero,
// sensor_type -- the shape a caller sends to rename one of several
// co-located sensors by explicitly naming which one.
func typedOverride(i2cAddr uint32, sensorType firmwarepb.SensorType, name string) *configpb.SensorConfig {
	return &configpb.SensorConfig{
		MuxPath:    noMux,
		I2CAddress: i2cAddr,
		SensorType: sensorType,
		Name:       name,
	}
}

// indexBySensorType indexes a composed sensor list at one i2c_address by
// sensor_type, for asserting on one of several co-located entries.
func indexBySensorType(sensors []*configpb.SensorConfig, i2cAddr uint32) map[firmwarepb.SensorType]*configpb.SensorConfig {
	out := make(map[firmwarepb.SensorType]*configpb.SensorConfig)
	for _, s := range sensors {
		if s.GetI2CAddress() == i2cAddr {
			out[s.GetSensorType()] = s
		}
	}
	return out
}

// TestComposeDesiredSensors_CoLocated_ExplicitSensorType_UpdatesOnlyThatOne
// is #1819's first required case: two inventory sensors co-located at one
// hardware address, differing only by sensor_type. An override that
// explicitly names sensor_type updates only that sensor -- the co-located
// sibling is carried through byte-identical, per FR8's "does not remove or
// reset any of the board's other sensors" guarantee (LB3).
func TestComposeDesiredSensors_CoLocated_ExplicitSensorType_UpdatesOnlyThatOne(t *testing.T) {
	inventory := coLocatedInventory()
	overrides := []*configpb.SensorConfig{
		typedOverride(coLocatedAddr, firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE, "sht3x_temp_renamed"),
	}

	got := ComposeDesiredSensors(inventory, nil, overrides)

	require.Len(t, got, 2, "both co-located sensors must survive the compose")
	byType := indexBySensorType(got, coLocatedAddr)

	temp := byType[firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE]
	require.NotNil(t, temp)
	assert.Equal(t, "sht3x_temp_renamed", temp.GetName(), "the explicitly-typed override must apply to the temperature sensor")

	humidity := byType[firmwarepb.SensorType_SENSOR_TYPE_HUMIDITY]
	require.NotNil(t, humidity)
	assert.True(t, proto.Equal(&configpb.SensorConfig{
		MuxPath:    noMux,
		I2CAddress: coLocatedAddr,
		Name:       "sht3x_humidity",
		SensorType: firmwarepb.SensorType_SENSOR_TYPE_HUMIDITY,
	}, humidity), "the untouched co-located humidity sensor must be byte-identical to its inventory state, not dropped or reset")
}

// TestTouchedSensorIDs_CoLocated_OnlyTouchedSensorReported is #1819's
// second required case: TouchedSensorIDs must return only the explicitly
// named co-located sensor's sensor_id, not its address-sharing sibling's --
// otherwise NFR4's retry-counter reset would re-arm the wrong sensor.
func TestTouchedSensorIDs_CoLocated_OnlyTouchedSensorReported(t *testing.T) {
	inventory := coLocatedInventory()
	overrides := []*configpb.SensorConfig{
		typedOverride(coLocatedAddr, firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE, "sht3x_temp_renamed"),
	}

	got := TouchedSensorIDs(inventory, overrides)

	assert.Equal(t, []int64{101}, got, "only the temperature sensor's sensor_id (101) must be reported, not the co-located humidity sibling's (102)")
}

// TestComposeDesiredSensors_CoLocated_AmbiguousOverride_NeitherSensorChanged
// is #1819's third required case: an override for a co-located address that
// omits sensor_type is genuinely ambiguous -- which of the >=2 candidates it
// means cannot be inferred. Per this package's chosen behavior (documented
// on ComposeDesiredSensors), such an override is applied to none of the
// co-located sensors: both survive, unchanged, rather than one being
// silently guessed or either being dropped.
func TestComposeDesiredSensors_CoLocated_AmbiguousOverride_NeitherSensorChanged(t *testing.T) {
	inventory := coLocatedInventory()
	overrides := []*configpb.SensorConfig{
		nameOverride(coLocatedAddr, "ambiguous_rename"), // sensor_type left unset
	}

	got := ComposeDesiredSensors(inventory, nil, overrides)

	require.Len(t, got, 2, "an ambiguous override must not drop either co-located sensor")
	byType := indexBySensorType(got, coLocatedAddr)
	assert.Equal(t, "sht3x_temp", byType[firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE].GetName(), "ambiguous override must not rename the temperature sensor")
	assert.Equal(t, "sht3x_humidity", byType[firmwarepb.SensorType_SENSOR_TYPE_HUMIDITY].GetName(), "ambiguous override must not rename the humidity sensor")
}

// TestTouchedSensorIDs_CoLocated_AmbiguousOverride_NoneReported mirrors the
// ambiguous case above for TouchedSensorIDs: an override that can't be
// resolved to one sensor must not report either co-located sensor_id as
// touched.
func TestTouchedSensorIDs_CoLocated_AmbiguousOverride_NoneReported(t *testing.T) {
	inventory := coLocatedInventory()
	overrides := []*configpb.SensorConfig{
		nameOverride(coLocatedAddr, "ambiguous_rename"),
	}

	got := TouchedSensorIDs(inventory, overrides)

	assert.Empty(t, got, "an ambiguous co-located override must not report either sensor_id as touched")
}

// TestComposeDesiredSensors_SingleSensorAtAddress_OverrideOmitsSensorType_Regression
// is #1819's required regression case: criterion 1's existing single-sensor
// (nameOverride, no sensor_type) behavior must keep passing unmodified even
// though matching now considers sensor_type for co-located addresses.
func TestComposeDesiredSensors_SingleSensorAtAddress_OverrideOmitsSensorType_Regression(t *testing.T) {
	inventory := []InventorySensor{inv(0x70, "single_sensor", nil)}
	overrides := []*configpb.SensorConfig{
		nameOverride(0x70, "single_sensor_renamed"),
	}

	got := ComposeDesiredSensors(inventory, nil, overrides)

	require.Len(t, got, 1)
	assert.Equal(t, "single_sensor_renamed", got[0].GetName(), "an override that omits sensor_type must still match a single sensor at that address")
}
