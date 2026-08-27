package config

import (
	"testing"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/hwkey"
)

// defaultBounds is a stand-in poll_interval_ms range shared by this file's
// tests -- never the zero value (see PollIntervalBounds' doc comment).
var defaultBounds = PollIntervalBounds{MinMs: 1000, MaxMs: 60000}

// TestFailureConstructors_NameEntryIndexAndField exhaustively covers FR59's
// "reports each failure against the specific offending entry and field":
// every one of the six failure constructors threads its entryIndex
// argument straight through to Failure.EntryIndex, and sets exactly the
// Field FR39 calls for -- a specific field name for the three per-field
// checks (i2c_address, sensor_type, poll_interval_ms), and "" (the whole
// entry, not one field of it) for the three whole-entry checks (a
// collision between two entries; a removal, which names removes[i] as a
// unit at the contract layer -- see config_push.go's
// validationFailureDetail, not this package).
func TestFailureConstructors_NameEntryIndexAndField(t *testing.T) {
	cases := []struct {
		name      string
		index     int
		failure   Failure
		wantField string
		wantClass FailureClass
	}{
		{"InvalidI2CAddress", 2, InvalidI2CAddressFailure(2, "msg-a"), "i2c_address", FailureInvalidI2CAddress},
		{"ChipTypeNotProduced", 5, ChipTypeNotProducedFailure(5, "msg-b"), "sensor_type", FailureChipTypeNotProduced},
		{"InvalidPollInterval", 1, InvalidPollIntervalFailure(1, "msg-c"), "poll_interval_ms", FailureInvalidPollInterval},
		{"HardwareKeyCollision", 3, HardwareKeyCollisionFailure(3, "msg-d"), "", FailureHardwareKeyCollision},
		{"UnknownRemoveKey", 4, UnknownRemoveKeyFailure(4, "msg-e"), "", FailureUnknownRemoveKey},
		{"UnaddressableRemove", 0, UnaddressableRemoveFailure(0, "msg-f"), "", FailureUnaddressableRemove},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.failure.EntryIndex != c.index {
				t.Errorf("EntryIndex = %d, want %d (the constructor's own argument)", c.failure.EntryIndex, c.index)
			}
			if c.failure.Field != c.wantField {
				t.Errorf("Field = %q, want %q", c.failure.Field, c.wantField)
			}
			if c.failure.Class != c.wantClass {
				t.Errorf("Class = %q, want %q", c.failure.Class, c.wantClass)
			}
			if c.failure.Message == "" {
				t.Error("Message is empty, want the caller-supplied text")
			}
		})
	}
}

// TestValidate_InvalidI2CAddressRange covers FR39's I2C address range
// check: below MinI2CAddress and above MaxI2CAddress fail, the boundary
// values themselves pass, and the legacy "unknown address" sentinel
// (i2c_address == 0) is exempt entirely (it is never actually treated as a
// bus address, see validate.go's doc comment on MinI2CAddress).
func TestValidate_InvalidI2CAddressRange(t *testing.T) {
	catalog := NewCatalog()
	cases := []struct {
		name        string
		addr        uint32
		wantFailure bool
	}{
		{"below range", 0x05, true},
		{"at min boundary", MinI2CAddress, false},
		{"at max boundary", MaxI2CAddress, false},
		{"above range", 0x78, true},
		{"zero sentinel exempt", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sc := &configpb.SensorConfig{I2CAddress: c.addr}
			adds := []Entry{{Key: CanonicalKey(sc, typeTemperature), Sensor: sc}}
			v := Validate(adds, nil, nil, catalog, defaultBounds)

			var found *Failure
			for i, f := range v.Failures {
				if f.Class == FailureInvalidI2CAddress {
					found = &v.Failures[i]
				}
			}
			if (found != nil) != c.wantFailure {
				t.Fatalf("invalid-i2c-address failure present = %v, want %v (Failures=%+v)", found != nil, c.wantFailure, v.Failures)
			}
			if found != nil {
				if found.EntryIndex != 0 {
					t.Errorf("EntryIndex = %d, want 0", found.EntryIndex)
				}
				if found.Field != "i2c_address" {
					t.Errorf("Field = %q, want %q", found.Field, "i2c_address")
				}
			}
		})
	}
}

// TestValidate_ChipMeasurementTypePair covers FR39's catalog check: a
// (chip_type, sensor_type) pair the catalog doesn't produce is a
// failure, a pair it does produce is not, and an entry with no chip_type
// (CHIP_TYPE_UNKNOWN -- config.proto's legacy patch-only case) or no
// resolved sensor_type is exempt from this check entirely, matching
// ChipTypeName/Validate's own documented exemptions.
func TestValidate_ChipMeasurementTypePair(t *testing.T) {
	catalog := NewCatalog()
	catalog.Add(ChipTypeName(configpb.ChipType_CHIP_TYPE_SHT3X), typeTemperature)

	cases := []struct {
		name         string
		chipType     configpb.ChipType
		sensorType   firmwarepb.SensorType
		sensorTypeID hwkey.SensorTypeID
		wantFailure  bool
	}{
		{"catalog produces this pair", configpb.ChipType_CHIP_TYPE_SHT3X, firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE, typeTemperature, false},
		{"catalog does not produce this pair", configpb.ChipType_CHIP_TYPE_SHT3X, firmwarepb.SensorType_SENSOR_TYPE_ECO2, typeECO2, true},
		{"chip_type unknown -- exempt (legacy patch-only entry)", configpb.ChipType_CHIP_TYPE_UNKNOWN, firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE, typeTemperature, false},
		{"sensor_type unknown -- exempt (unresolved, not this check's job)", configpb.ChipType_CHIP_TYPE_SHT3X, firmwarepb.SensorType_SENSOR_TYPE_UNKNOWN, hwkey.SensorTypeID(0), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sc := &configpb.SensorConfig{I2CAddress: 0x44, ChipType: c.chipType, SensorType: c.sensorType}
			adds := []Entry{{Key: CanonicalKey(sc, c.sensorTypeID), Sensor: sc}}
			v := Validate(adds, nil, nil, catalog, defaultBounds)

			var found *Failure
			for i, f := range v.Failures {
				if f.Class == FailureChipTypeNotProduced {
					found = &v.Failures[i]
				}
			}
			if (found != nil) != c.wantFailure {
				t.Fatalf("chip-type-not-produced failure present = %v, want %v (Failures=%+v)", found != nil, c.wantFailure, v.Failures)
			}
			if found != nil {
				if found.EntryIndex != 0 {
					t.Errorf("EntryIndex = %d, want 0", found.EntryIndex)
				}
				if found.Field != "sensor_type" {
					t.Errorf("Field = %q, want %q", found.Field, "sensor_type")
				}
			}
		})
	}
}

// TestValidate_PollIntervalBounds covers FR39's poll_interval_ms check
// against the stated min/max: below min and above max fail, the
// boundaries pass, and 0 (never authored) is exempt.
func TestValidate_PollIntervalBounds(t *testing.T) {
	catalog := NewCatalog()
	cases := []struct {
		name        string
		ms          uint32
		wantFailure bool
	}{
		{"below min", 500, true},
		{"at min boundary", defaultBounds.MinMs, false},
		{"at max boundary", defaultBounds.MaxMs, false},
		{"above max", 120000, true},
		{"zero -- exempt (unset)", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sc := &configpb.SensorConfig{I2CAddress: 0x44, PollIntervalMs: c.ms}
			adds := []Entry{{Key: CanonicalKey(sc, typeTemperature), Sensor: sc}}
			v := Validate(adds, nil, nil, catalog, defaultBounds)

			var found *Failure
			for i, f := range v.Failures {
				if f.Class == FailureInvalidPollInterval {
					found = &v.Failures[i]
				}
			}
			if (found != nil) != c.wantFailure {
				t.Fatalf("invalid-poll-interval failure present = %v, want %v (Failures=%+v)", found != nil, c.wantFailure, v.Failures)
			}
			if found != nil {
				if found.EntryIndex != 0 {
					t.Errorf("EntryIndex = %d, want 0", found.EntryIndex)
				}
				if found.Field != "poll_interval_ms" {
					t.Errorf("Field = %q, want %q", found.Field, "poll_interval_ms")
				}
			}
		})
	}
}

// TestValidate_HardwareKeyCollision_HexAndDecimalSameAddress covers FR39's
// within-payload collision check plus FR18.2's stated worked example: an
// entry addressed 0x1A and an entry addressed 26 name the same hardware
// key (they are numerically identical) and must collide, naming the
// second entry against the first.
func TestValidate_HardwareKeyCollision_HexAndDecimalSameAddress(t *testing.T) {
	catalog := NewCatalog()
	a := &configpb.SensorConfig{I2CAddress: 0x1A}
	b := &configpb.SensorConfig{I2CAddress: 26}
	adds := []Entry{
		{Key: CanonicalKey(a, typeTemperature), Sensor: a},
		{Key: CanonicalKey(b, typeTemperature), Sensor: b},
	}
	v := Validate(adds, nil, nil, catalog, defaultBounds)

	var collisions []Failure
	for _, f := range v.Failures {
		if f.Class == FailureHardwareKeyCollision {
			collisions = append(collisions, f)
		}
	}
	if len(collisions) != 1 {
		t.Fatalf("collision failures = %d, want 1 (0x1A and 26 name the same hardware key): %+v", len(collisions), v.Failures)
	}
	if collisions[0].EntryIndex != 1 {
		t.Errorf("EntryIndex = %d, want 1 (the second entry, naming sensors[0] as the one it collides with)", collisions[0].EntryIndex)
	}
}

// TestValidate_ThreeWayCollision_EachLaterEntryNamesTheFirst covers a
// within-payload collision among more than two entries: every entry after
// the first sharing a key is its own failure, each naming the first entry
// as the one it collides with -- proving a single collision does not
// consume or mask a later one at the same key.
func TestValidate_ThreeWayCollision_EachLaterEntryNamesTheFirst(t *testing.T) {
	catalog := NewCatalog()
	a := &configpb.SensorConfig{I2CAddress: 0x44}
	b := &configpb.SensorConfig{I2CAddress: 0x44}
	c := &configpb.SensorConfig{I2CAddress: 0x44}
	adds := []Entry{
		{Key: CanonicalKey(a, typeTemperature), Sensor: a},
		{Key: CanonicalKey(b, typeTemperature), Sensor: b},
		{Key: CanonicalKey(c, typeTemperature), Sensor: c},
	}
	v := Validate(adds, nil, nil, catalog, defaultBounds)

	var collisionIndices []int
	for _, f := range v.Failures {
		if f.Class == FailureHardwareKeyCollision {
			collisionIndices = append(collisionIndices, f.EntryIndex)
		}
	}
	if len(collisionIndices) != 2 {
		t.Fatalf("collision failures = %d, want 2 (sensors[1] and sensors[2] each colliding with sensors[0])", len(collisionIndices))
	}
	if collisionIndices[0] != 1 || collisionIndices[1] != 2 {
		t.Errorf("collision EntryIndexes = %v, want [1 2]", collisionIndices)
	}
}

// TestValidate_SwapWithinPayload_NotCaughtByCollisionCheck asserts the
// documented boundary with FR16.4: two entries in one payload that each
// take on a different address than they previously had (a "swap") produce
// two distinct canonical keys, not a within-payload collision -- Validate
// must not flag this. Swap handling (refusing an unresolved swap) belongs
// to checkPushConfigIdentity (the Phase 3 identity task), never
// duplicated or assumed away here.
func TestValidate_SwapWithinPayload_NotCaughtByCollisionCheck(t *testing.T) {
	catalog := NewCatalog()
	// sensor-a now claims 0x20 (previously 0x10); sensor-b now claims 0x10
	// (previously 0x20) -- a swap, but each entry's own key (0x20 and
	// 0x10) is distinct from the other's, so this is not a collision.
	sensorA := &configpb.SensorConfig{Name: "sensor-a", I2CAddress: 0x20}
	sensorB := &configpb.SensorConfig{Name: "sensor-b", I2CAddress: 0x10}
	adds := []Entry{
		{Key: CanonicalKey(sensorA, typeTemperature), Sensor: sensorA},
		{Key: CanonicalKey(sensorB, typeTemperature), Sensor: sensorB},
	}
	v := Validate(adds, nil, nil, catalog, defaultBounds)

	for _, f := range v.Failures {
		if f.Class == FailureHardwareKeyCollision {
			t.Fatalf("Validate flagged a within-payload swap as a hardware key collision: %+v -- a swap produces two distinct canonical keys, not a collision (FR16.4 boundary)", f)
		}
	}
	if !v.OK() {
		t.Errorf("Validate found unexpected failures for a clean swap payload: %+v", v.Failures)
	}
}

// TestValidate_MultipleFailures_AllReturnedTogether covers FR39's "a
// single failure must not mask the rest": a payload failing three
// independent checks (address range, catalog pair, poll interval) at
// once must surface every one of them, not just the first found.
func TestValidate_MultipleFailures_AllReturnedTogether(t *testing.T) {
	catalog := NewCatalog() // produces nothing at all

	badAddr := &configpb.SensorConfig{I2CAddress: 0x03}                                                                                                         // out of range
	badChip := &configpb.SensorConfig{I2CAddress: 0x30, ChipType: configpb.ChipType_CHIP_TYPE_SHT3X, SensorType: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE} // empty catalog -> not produced
	badPoll := &configpb.SensorConfig{I2CAddress: 0x40, PollIntervalMs: 5}                                                                                      // below min

	adds := []Entry{
		{Key: CanonicalKey(badAddr, typeIlluminance), Sensor: badAddr},
		{Key: CanonicalKey(badChip, typeTemperature), Sensor: badChip},
		{Key: CanonicalKey(badPoll, typeHumidity), Sensor: badPoll},
	}
	v := Validate(adds, nil, nil, catalog, defaultBounds)

	if v.OK() {
		t.Fatal("Validate reported OK for a payload with three independent failures")
	}

	wantClasses := map[FailureClass]bool{
		FailureInvalidI2CAddress:   false,
		FailureChipTypeNotProduced: false,
		FailureInvalidPollInterval: false,
	}
	for _, f := range v.Failures {
		if _, tracked := wantClasses[f.Class]; tracked {
			wantClasses[f.Class] = true
		}
	}
	for class, found := range wantClasses {
		if !found {
			t.Errorf("missing failure class %q -- a single failure must not mask the rest (FR39)", class)
		}
	}
	if len(v.Failures) < 3 {
		t.Errorf("len(Failures) = %d, want at least 3 (one per independent check)", len(v.Failures))
	}
}

// TestValidate_RemoveMatchingNothing_IsError covers FR82.4/FR39's explicit
// anti-silence rule: a remove key matching no entry in base is an error
// against that key, never a silent no-op.
func TestValidate_RemoveMatchingNothing_IsError(t *testing.T) {
	catalog := NewCatalog()
	base := []Entry{entryAt("temp", 0x44, typeTemperature, nil, ProvenanceAuthored)}
	removes := []RemoveKey{removeFullKey(0x99, typeTemperature, nil)} // matches nothing in base

	v := Validate(nil, removes, base, catalog, defaultBounds)

	if v.OK() {
		t.Fatal("Validate reported OK for a remove matching nothing -- want an error, never a silent no-op (FR82.4/FR39)")
	}
	var found *Failure
	for i, f := range v.Failures {
		if f.Class == FailureUnknownRemoveKey {
			found = &v.Failures[i]
		}
	}
	if found == nil {
		t.Fatalf("no FailureUnknownRemoveKey found: %+v", v.Failures)
	}
	if found.EntryIndex != 0 {
		t.Errorf("EntryIndex = %d, want 0", found.EntryIndex)
	}
}

// TestValidate_RemoveMatchingSomething_NoError proves the anti-silence
// check above doesn't misfire on a remove that does match -- the negative
// case for TestValidate_RemoveMatchingNothing_IsError.
func TestValidate_RemoveMatchingSomething_NoError(t *testing.T) {
	catalog := NewCatalog()
	base := []Entry{entryAt("temp", 0x44, typeTemperature, nil, ProvenanceAuthored)}
	removes := []RemoveKey{removeFullKey(0x44, typeTemperature, nil)} // matches base[0]

	v := Validate(nil, removes, base, catalog, defaultBounds)
	if !v.OK() {
		t.Errorf("Validate found unexpected failures for a remove that matches an entry in base: %+v", v.Failures)
	}
}

// TestValidate_UnaddressableRemove_NamesReasonAndRemedy covers FR82.4's
// second removal-validation case: a remove naming an entry with no I2C
// address on record (the legacy "unknown address" sentinel) is rejected
// with the named failure class, and its message states the remedy
// (FR82.4's stated push scope=COMPLETE alternative) -- checked as a
// static property of the remove key itself, so it's caught even with an
// empty/nil base.
func TestValidate_UnaddressableRemove_NamesReasonAndRemedy(t *testing.T) {
	catalog := NewCatalog()
	removes := []RemoveKey{removeChipKey(0, nil)} // the unknown-address sentinel

	v := Validate(nil, removes, nil, catalog, defaultBounds)

	if v.OK() {
		t.Fatal("Validate reported OK for a remove naming an unaddressable entry")
	}
	var found *Failure
	for i, f := range v.Failures {
		if f.Class == FailureUnaddressableRemove {
			found = &v.Failures[i]
		}
	}
	if found == nil {
		t.Fatalf("no FailureUnaddressableRemove found: %+v", v.Failures)
	}
	if found.EntryIndex != 0 {
		t.Errorf("EntryIndex = %d, want 0", found.EntryIndex)
	}
	// The "unknown remove key" failure must not also fire for the same
	// entry -- it's unaddressable, not merely unmatched.
	for _, f := range v.Failures {
		if f.Class == FailureUnknownRemoveKey {
			t.Errorf("an unaddressable remove also produced FailureUnknownRemoveKey: %+v", f)
		}
	}
}

// TestValidate_CleanPayload_OK is the negative-space control: a payload
// that fails none of FR39's checks validates cleanly.
func TestValidate_CleanPayload_OK(t *testing.T) {
	catalog := NewCatalog()
	catalog.Add(ChipTypeName(configpb.ChipType_CHIP_TYPE_SHT3X), typeTemperature)

	sc := &configpb.SensorConfig{
		I2CAddress:     0x44,
		ChipType:       configpb.ChipType_CHIP_TYPE_SHT3X,
		SensorType:     firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE,
		PollIntervalMs: 5000,
	}
	adds := []Entry{{Key: CanonicalKey(sc, typeTemperature), Sensor: sc}}

	v := Validate(adds, nil, nil, catalog, defaultBounds)
	if !v.OK() {
		t.Errorf("Validate found unexpected failures for a clean payload: %+v", v.Failures)
	}
}
