package config

import (
	"errors"
	"testing"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/hwkey"
)

// TestRemoveKeyFromProto_SensorTypePresent_IsFullKey proves the wire
// RemoveKey's `optional sensor_type` presence, not merely its value,
// distinguishes a full canonical key from a chip key -- SENSOR_TYPE_UNKNOWN
// (proto zero value) explicitly set must still count as "present".
func TestRemoveKeyFromProto_SensorTypePresent_IsFullKey(t *testing.T) {
	st := firmwarepb.SensorType_SENSOR_TYPE_UNKNOWN
	rk := &pb.RemoveKey{I2CAddress: 0x44, SensorType: &st}

	got := RemoveKeyFromProto(rk, hwkey.SensorTypeID(9), true)
	if !got.HasSensorType {
		t.Fatal("RemoveKeyFromProto with sensor_type explicitly set (even SENSOR_TYPE_UNKNOWN) produced HasSensorType=false")
	}
	if got.Form() != RemoveFormFullKey {
		t.Errorf("Form() = %v, want RemoveFormFullKey", got.Form())
	}
}

// TestRemoveKeyFromProto_SensorTypeAbsent_IsChipKey proves an omitted
// sensor_type field (the wire message's `optional` left unset) becomes a
// chip key.
func TestRemoveKeyFromProto_SensorTypeAbsent_IsChipKey(t *testing.T) {
	rk := &pb.RemoveKey{I2CAddress: 0x44}

	got := RemoveKeyFromProto(rk, 0, false)
	if got.HasSensorType {
		t.Fatal("RemoveKeyFromProto with sensor_type unset produced HasSensorType=true")
	}
	if got.Form() != RemoveFormChipKey {
		t.Errorf("Form() = %v, want RemoveFormChipKey", got.Form())
	}
}

// TestRemoveKey_Match_FullKey_MatchesAtMostOneEntry proves a full
// canonical key selects exactly the entry at (i2c_address, mux_path,
// sensor_type) and nothing else, even when a sibling entry shares two of
// the three components.
func TestRemoveKey_Match_FullKey_MatchesAtMostOneEntry(t *testing.T) {
	base := []Entry{
		entryAt("temperature", 0x44, typeTemperature, nil, ""),
		entryAt("humidity", 0x44, typeHumidity, nil, ""),
		entryAt("elsewhere-temp", 0x45, typeTemperature, nil, ""),
	}
	k := removeFullKey(0x44, typeTemperature, nil)

	matched, err := k.Match(base)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matched) != 1 || matched[0].Sensor.Name != "temperature" {
		t.Fatalf("Match = %+v, want only 'temperature'", matched)
	}
}

// TestRemoveKey_Match_ChipKey_MatchesEveryEntryAtThatChip proves a chip
// key (sensor_type absent) matches every entry sharing the chip's
// (i2c_address, mux_path), regardless of sensor_type.
func TestRemoveKey_Match_ChipKey_MatchesEveryEntryAtThatChip(t *testing.T) {
	base := []Entry{
		entryAt("temperature", 0x44, typeTemperature, nil, ""),
		entryAt("humidity", 0x44, typeHumidity, nil, ""),
		entryAt("elsewhere-temp", 0x45, typeTemperature, nil, ""),
	}
	k := removeChipKey(0x44, nil)

	matched, err := k.Match(base)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matched) != 2 {
		t.Fatalf("len(Match) = %d, want 2 (temperature + humidity)", len(matched))
	}
	names := map[string]bool{matched[0].Sensor.Name: true, matched[1].Sensor.Name: true}
	if !names["temperature"] || !names["humidity"] {
		t.Errorf("Match = %+v, want temperature and humidity", matched)
	}
}

// TestRemoveKey_Match_UnknownAddressSentinel_ReturnsErrUnaddressableRemove
// proves a RemoveKey naming the unknown-address sentinel (i2c_address==0)
// is refused before any matching is attempted -- FR82.4's stated EDIT
// limitation, not a silent no-op -- even when a base entry would otherwise
// match it structurally.
func TestRemoveKey_Match_UnknownAddressSentinel_ReturnsErrUnaddressableRemove(t *testing.T) {
	base := []Entry{entryAt("mystery", 0, typeIlluminance, nil, "")}
	k := removeFullKey(0, typeIlluminance, nil)

	_, err := k.Match(base)
	if !errors.Is(err, ErrUnaddressableRemove) {
		t.Fatalf("Match error = %v, want ErrUnaddressableRemove", err)
	}
}

// TestRemoveKey_Match_MuxPathDistinguishesOtherwiseIdenticalChips proves
// two chips at the same i2c_address but different mux_path never collide
// -- Match must compare mux_path, not just i2c_address.
func TestRemoveKey_Match_MuxPathDistinguishesOtherwiseIdenticalChips(t *testing.T) {
	muxA := hwkey.MuxPath{{MuxAddress: 0x70, MuxChannel: 0}}
	muxB := hwkey.MuxPath{{MuxAddress: 0x70, MuxChannel: 1}}
	base := []Entry{
		entryAt("light-a", 0x23, typeIlluminance, muxA, ""),
		entryAt("light-b", 0x23, typeIlluminance, muxB, ""),
	}
	k := removeChipKey(0x23, muxA)

	matched, err := k.Match(base)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matched) != 1 || matched[0].Sensor.Name != "light-a" {
		t.Fatalf("Match = %+v, want only 'light-a'", matched)
	}
}
