package config

import (
	"errors"
	"testing"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/hwkey"
)

// Fixed sensor_type_id values standing in for the catalog, matching this
// task's own worked examples (FR82.4): an SHT3x chip is temperature (10)
// and humidity (11) at one address; a CCS811 is eCO2 (20) and TVOC (21).
const (
	typeTemperature = hwkey.SensorTypeID(10)
	typeHumidity    = hwkey.SensorTypeID(11)
	typeECO2        = hwkey.SensorTypeID(20)
	typeTVOC        = hwkey.SensorTypeID(21)
	typeIlluminance = hwkey.SensorTypeID(30)
)

func entryAt(name string, i2c uint16, sensorTypeID hwkey.SensorTypeID, mux hwkey.MuxPath, provenance Provenance) Entry {
	return Entry{
		Key: hwkey.Key{
			I2CAddress:   hwkey.Address(i2c),
			MuxPath:      mux,
			SensorTypeID: sensorTypeID,
		},
		Sensor:     &configpb.SensorConfig{Name: name, I2CAddress: uint32(i2c)},
		Provenance: provenance,
	}
}

func removeFullKey(i2c uint16, sensorTypeID hwkey.SensorTypeID, mux hwkey.MuxPath) RemoveKey {
	return RemoveKey{
		Chip:          hwkey.ChipKey{I2CAddress: hwkey.Address(i2c), MuxPath: mux},
		SensorTypeID:  sensorTypeID,
		HasSensorType: true,
	}
}

func removeChipKey(i2c uint16, mux hwkey.MuxPath) RemoveKey {
	return RemoveKey{
		Chip:          hwkey.ChipKey{I2CAddress: hwkey.Address(i2c), MuxPath: mux},
		HasSensorType: false,
	}
}

// TestMaterialise_NilBase_ReturnsErrNoAcceptedConfig covers FR82.3's exact
// refusal condition at the pure-logic layer: base == nil (never a non-nil
// empty slice) is Materialise's documented signal for "no accepted config
// exists for this board at all" -- adds/removes are never even consulted.
func TestMaterialise_NilBase_ReturnsErrNoAcceptedConfig(t *testing.T) {
	adds := []Entry{entryAt("light", 0x23, typeIlluminance, nil, "")}
	_, err := Materialise(nil, adds, nil)
	if !errors.Is(err, ErrNoAcceptedConfig) {
		t.Fatalf("Materialise(nil base, ...) error = %v, want ErrNoAcceptedConfig", err)
	}
}

// TestMaterialise_EmptyNonNilBase_IsAValidEditBase proves a genuinely-empty
// accepted config (base is a non-nil, zero-length slice) is a legitimate
// EDIT base -- distinct from "no accepted config at all" -- and adds still
// apply against it.
func TestMaterialise_EmptyNonNilBase_IsAValidEditBase(t *testing.T) {
	base := []Entry{}
	adds := []Entry{entryAt("light", 0x23, typeIlluminance, nil, "")}
	result, err := Materialise(base, adds, nil)
	if err != nil {
		t.Fatalf("Materialise with empty non-nil base returned an error, want success: %v", err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("len(Entries) = %d, want 1", len(result.Entries))
	}
	if result.Entries[0].Provenance != ProvenanceAuthored {
		t.Errorf("Provenance = %q, want %q", result.Entries[0].Provenance, ProvenanceAuthored)
	}
}

// TestMaterialise_UnnamedBaseEntriesCarryForwardMaterialised is FR82.3's
// core EDIT contract: an entry named in neither adds nor removes is
// unchanged and materialised server-side, tagged ProvenanceMaterialised;
// an entry the caller did name is tagged ProvenanceAuthored.
func TestMaterialise_UnnamedBaseEntriesCarryForwardMaterialised(t *testing.T) {
	base := []Entry{
		entryAt("light", 0x23, typeIlluminance, nil, ProvenanceAuthored), // untouched
		entryAt("temp", 0x44, typeTemperature, nil, ProvenanceAuthored),  // will be renamed by an add
	}
	adds := []Entry{
		entryAt("temperature", 0x44, typeTemperature, nil, ""), // same key as base[1], new name
	}

	result, err := Materialise(base, adds, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("len(Entries) = %d, want 2", len(result.Entries))
	}

	byName := map[string]Entry{}
	for _, e := range result.Entries {
		byName[e.Sensor.Name] = e
	}

	light, ok := byName["light"]
	if !ok {
		t.Fatal("materialised result is missing the untouched 'light' entry")
	}
	if light.Provenance != ProvenanceMaterialised {
		t.Errorf("'light' Provenance = %q, want %q (unnamed, carried forward)", light.Provenance, ProvenanceMaterialised)
	}

	temperature, ok := byName["temperature"]
	if !ok {
		t.Fatal("materialised result is missing the renamed 'temperature' entry")
	}
	if temperature.Provenance != ProvenanceAuthored {
		t.Errorf("'temperature' Provenance = %q, want %q (named in adds)", temperature.Provenance, ProvenanceAuthored)
	}

	if _, stillThere := byName["temp"]; stillThere {
		t.Error("stale 'temp' entry (superseded by an add at the same key) is still present in the result")
	}
}

// TestMaterialise_FullCanonicalKeyRemove_DropsExactlyOneEntry covers
// FR82.4: a remove naming a full canonical key (sensor_type present) drops
// only the one entry at that exact key -- a sibling entry sharing the same
// chip (same i2c_address/mux_path, different sensor_type) survives.
func TestMaterialise_FullCanonicalKeyRemove_DropsExactlyOneEntry(t *testing.T) {
	base := []Entry{
		entryAt("temperature", 0x44, typeTemperature, nil, ""), // SHT3x temp
		entryAt("humidity", 0x44, typeHumidity, nil, ""),       // SHT3x humidity, same chip
	}
	removes := []RemoveKey{removeFullKey(0x44, typeTemperature, nil)}

	result, err := Materialise(base, nil, removes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Entries) != 1 || result.Entries[0].Sensor.Name != "humidity" {
		t.Fatalf("Entries = %+v, want only 'humidity' to survive", result.Entries)
	}
	if len(result.Removed) != 1 {
		t.Fatalf("len(Removed) = %d, want 1", len(result.Removed))
	}
	if result.Removed[0].Entry.Sensor.Name != "temperature" {
		t.Errorf("Removed[0].Entry.Sensor.Name = %q, want %q", result.Removed[0].Entry.Sensor.Name, "temperature")
	}
	if result.Removed[0].Form != RemoveFormFullKey {
		t.Errorf("Removed[0].Form = %v, want RemoveFormFullKey", result.Removed[0].Form)
	}
}

// TestMaterialise_ChipKeyRemove_DropsEveryEntryAtThatAddressAndMuxPath
// covers FR82.4's chip-key removal form: removing a CCS811 by chip key
// (sensor_type absent) drops both its eCO2 and TVOC entries -- "the form
// that matches the physical action of unsoldering a part".
func TestMaterialise_ChipKeyRemove_DropsEveryEntryAtThatAddressAndMuxPath(t *testing.T) {
	base := []Entry{
		entryAt("eco2", 0x5A, typeECO2, nil, ""),
		entryAt("tvoc", 0x5A, typeTVOC, nil, ""),
		entryAt("light", 0x23, typeIlluminance, nil, ""), // different chip entirely, must survive
	}
	removes := []RemoveKey{removeChipKey(0x5A, nil)}

	result, err := Materialise(base, nil, removes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Entries) != 1 || result.Entries[0].Sensor.Name != "light" {
		t.Fatalf("Entries = %+v, want only 'light' to survive", result.Entries)
	}
	if len(result.Removed) != 2 {
		t.Fatalf("len(Removed) = %d, want 2 (eco2 + tvoc)", len(result.Removed))
	}
	for _, r := range result.Removed {
		if r.Form != RemoveFormChipKey {
			t.Errorf("Removed entry %q Form = %v, want RemoveFormChipKey", r.Entry.Sensor.Name, r.Form)
		}
	}
}

// TestMaterialise_UnaddressableRemove_UnknownSentinel_ReturnsErrUnaddressableRemove
// covers FR82.4/FR39: a remove naming the legacy "unknown address"
// sentinel (i2c_address == 0) is refused, not a silent no-op -- such an
// entry has no removal path via EDIT at all.
func TestMaterialise_UnaddressableRemove_UnknownSentinel_ReturnsErrUnaddressableRemove(t *testing.T) {
	base := []Entry{entryAt("mystery", 0, typeIlluminance, nil, "")}
	removes := []RemoveKey{removeChipKey(0, nil)}

	_, err := Materialise(base, nil, removes)
	if !errors.Is(err, ErrUnaddressableRemove) {
		t.Fatalf("Materialise error = %v, want ErrUnaddressableRemove", err)
	}
}

// TestMaterialise_AddWinsOverRemove_EntrySurvives covers the documented
// tie-break: a key present in both adds and a remove's match set is kept
// (the add wins), and does not appear in Result.Removed -- its
// hardware-history interval must not be closed, since the entry is still
// present.
func TestMaterialise_AddWinsOverRemove_EntrySurvives(t *testing.T) {
	base := []Entry{entryAt("temp", 0x44, typeTemperature, nil, "")}
	adds := []Entry{entryAt("temperature", 0x44, typeTemperature, nil, "")} // same key, renamed
	removes := []RemoveKey{removeFullKey(0x44, typeTemperature, nil)}       // would also match it

	result, err := Materialise(base, adds, removes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Entries) != 1 || result.Entries[0].Sensor.Name != "temperature" {
		t.Fatalf("Entries = %+v, want the add ('temperature') to win", result.Entries)
	}
	if len(result.Removed) != 0 {
		t.Errorf("Removed = %+v, want empty -- an add at the same key must not count as removed", result.Removed)
	}
}

// TestMaterialise_FiveAddsPlusTwoRemoves_OneResultSet proves multiple
// edits and removals combine into one Result -- FR82.5's "one config
// version per submission" is a property of how the handler calls
// Materialise exactly once per push with the whole adds/removes list, not
// something Materialise itself could violate, but this pins the combined
// accounting is correct for a non-trivial batch.
func TestMaterialise_FiveAddsPlusTwoRemoves_OneResultSet(t *testing.T) {
	base := []Entry{
		entryAt("keep-1", 0x01, typeIlluminance, nil, ""),
		entryAt("drop-1", 0x02, typeIlluminance, nil, ""),
		entryAt("drop-2-a", 0x03, typeECO2, nil, ""),
		entryAt("drop-2-b", 0x03, typeTVOC, nil, ""),
	}
	adds := []Entry{
		entryAt("add-1", 0x10, typeIlluminance, nil, ""),
		entryAt("add-2", 0x11, typeIlluminance, nil, ""),
		entryAt("add-3", 0x12, typeIlluminance, nil, ""),
		entryAt("add-4", 0x13, typeIlluminance, nil, ""),
		entryAt("add-5", 0x14, typeIlluminance, nil, ""),
	}
	removes := []RemoveKey{
		removeFullKey(0x02, typeIlluminance, nil), // drop-1
		removeChipKey(0x03, nil),                  // drop-2-a, drop-2-b
	}

	result, err := Materialise(base, adds, removes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// keep-1 (materialised) + 5 adds (authored) = 6 surviving entries.
	if len(result.Entries) != 6 {
		t.Fatalf("len(Entries) = %d, want 6", len(result.Entries))
	}
	if len(result.Removed) != 3 {
		t.Fatalf("len(Removed) = %d, want 3 (drop-1, drop-2-a, drop-2-b)", len(result.Removed))
	}
}
