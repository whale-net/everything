package main

import (
	"context"
	"log/slog"
	"testing"
	"time"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/hwkey"
	"github.com/whale-net/everything/leaflab/invalidation"
	"google.golang.org/protobuf/proto"
)

// stubRepo records UpsertSensor and ApplyConfigRegions calls so tests can assert behaviour.
type stubRepo struct {
	// Configurable return values.
	boardID      int64
	sensorTypeID int64
	sensorID     int64
	// applyConfigRegionsSkips is returned verbatim by every ApplyConfigRegions
	// call -- tests configure this to exercise handleConfigAck's skip-surfacing
	// path without a real re-validation implementation behind it.
	applyConfigRegionsSkips []RegionApplySkip

	// Configurable board sensor identity snapshot returned by
	// LoadBoardSensorIdentities -- empty by default so existing tests (none
	// of which exercise FR16.3's elimination step) always fall through to
	// UpsertSensor, unchanged from before this field existed.
	boardSensorIdentities []BoardSensorIdentity

	// getSensorResults, keyed "deviceID/sensorName", is what GetSensor
	// returns when present -- lets a test simulate the DB observing a
	// region/identity/name change committed by another writer (FR73's
	// cache-miss/invalidation-driven re-read path in handleSensorReading)
	// without a real database. A key absent from this map falls back to
	// (SensorInfo{}, false, nil), exactly stubRepo.GetSensor's behaviour
	// before this field existed -- so every pre-existing test (none of
	// which populates it) is unaffected.
	getSensorResults map[string]SensorInfo

	// applyConfigRegionsResult is what ApplyConfigRegions returns -- lets a
	// test simulate this process's own config-apply writer (FR73's second
	// writer surface: "the processor's own ApplyConfigRegions") reporting a
	// real region change, instead of the empty default every pre-existing
	// test (which doesn't set this field) relies on.
	applyConfigRegionsResult []RegionChange

	// Recorded call arguments.
	upsertSensorCalls          []upsertSensorCall
	upsertSensorHWHistoryCalls []upsertSensorHWHistoryCall
	applyConfigRegionsCalls    []applyConfigRegionsCall
	rewireAndRenameSensorCalls []rewireAndRenameSensorCall
	insertReadingCalls         []insertReadingCall
}

type insertReadingCall struct {
	sensorID int64
	regionID *int64
	value    float64
}

type rewireAndRenameSensorCall struct {
	sensorID     int64
	sensorTypeID int64
	name         string
	unit         string
	hw           *HardwareAddress
}

type upsertSensorHWHistoryCall struct {
	sensorID int64
	hw       *HardwareAddress
}

type applyConfigRegionsCall struct {
	boardID int64
	version int64
}

type upsertSensorCall struct {
	boardID      int64
	sensorTypeID int64
	name         string
	unit         string
	hw           *HardwareAddress
}

func (s *stubRepo) UpsertBoard(_ context.Context, _ string) (int64, error) {
	return s.boardID, nil
}

func (s *stubRepo) UpsertSensorType(_ context.Context, _, _ string) (int64, error) {
	return s.sensorTypeID, nil
}

func (s *stubRepo) UpsertSensor(_ context.Context, boardID, sensorTypeID int64, name, unit string, hw *HardwareAddress) (int64, *int64, error) {
	s.upsertSensorCalls = append(s.upsertSensorCalls, upsertSensorCall{
		boardID:      boardID,
		sensorTypeID: sensorTypeID,
		name:         name,
		unit:         unit,
		hw:           hw,
	})
	return s.sensorID, nil, nil
}

func (s *stubRepo) LoadBoardSensorIdentities(_ context.Context, _ int64) ([]BoardSensorIdentity, error) {
	return s.boardSensorIdentities, nil
}

func (s *stubRepo) RewireAndRenameSensor(_ context.Context, sensorID, sensorTypeID int64, name, unit string, hw *HardwareAddress) (*int64, error) {
	s.rewireAndRenameSensorCalls = append(s.rewireAndRenameSensorCalls, rewireAndRenameSensorCall{
		sensorID:     sensorID,
		sensorTypeID: sensorTypeID,
		name:         name,
		unit:         unit,
		hw:           hw,
	})
	return nil, nil
}

func (s *stubRepo) UpsertSensorLabel(_ context.Context, _ int64, _ string) error { return nil }

func (s *stubRepo) UpsertSensorHWHistory(_ context.Context, sensorID int64, hw *HardwareAddress) error {
	s.upsertSensorHWHistoryCalls = append(s.upsertSensorHWHistoryCalls, upsertSensorHWHistoryCall{sensorID: sensorID, hw: hw})
	return nil
}

func (s *stubRepo) GetSensor(_ context.Context, deviceID, sensorName string) (SensorInfo, bool, error) {
	if info, ok := s.getSensorResults[deviceID+"/"+sensorName]; ok {
		return info, true, nil
	}
	return SensorInfo{}, false, nil
}

func (s *stubRepo) InsertReading(_ context.Context, sensorID int64, regionID *int64, value float64, _ bool, _ uint32, _ time.Time, _ *int64) error {
	s.insertReadingCalls = append(s.insertReadingCalls, insertReadingCall{sensorID: sensorID, regionID: regionID, value: value})
	return nil
}

func (s *stubRepo) UpsertDeviceConfig(_ context.Context, _ int64, _ int64, _ []byte) error {
	return nil
}

func (s *stubRepo) AckDeviceConfig(_ context.Context, _ int64, _ int64, _ bool, _ string) (time.Time, time.Time, error) {
	return time.Time{}, time.Time{}, nil
}

func (s *stubRepo) ApplyConfigRegions(_ context.Context, boardID, version int64) ([]RegionApplySkip, []RegionChange, error) {
	s.applyConfigRegionsCalls = append(s.applyConfigRegionsCalls, applyConfigRegionsCall{boardID: boardID, version: version})
	return s.applyConfigRegionsSkips, s.applyConfigRegionsResult, nil
}

func (s *stubRepo) CloseRemovedSensorHWHistory(_ context.Context, _ int64, _ int64) error {
	return nil
}

func (s *stubRepo) SetSensorChipID(_ context.Context, _ int64, _ string) error { return nil }

func (s *stubRepo) IsKnownChipAddress(_ context.Context, _ string, _ uint32) (bool, error) {
	return true, nil
}

// marshalManifest encodes a DeviceManifest to wire bytes.
func marshalManifest(t *testing.T, m *firmwarepb.DeviceManifest) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return b
}

func newTestHandler(repo SensorRepository) *MessageHandler {
	// invalidationPub is nil: none of this file's tests exercise FR73's
	// cross-process broadcast (that needs a real broker -- see
	// leaflab/invalidation's BUILD.bazel doc comment), and both call sites
	// that use it (RewireSensor's caller in leaflab/api, handleConfigAck
	// here) nil-check before publishing.
	return NewMessageHandler(slog.Default(), repo, NewSensorCache(), nil)
}

// TestHandleManifest_HWAddressPassedThrough verifies that when a SensorDescriptor
// carries a non-zero i2c_address, handleManifest passes a populated *HardwareAddress
// to UpsertSensor.
func TestHandleManifest_HWAddressPassedThrough(t *testing.T) {
	repo := &stubRepo{boardID: 1, sensorTypeID: 2, sensorID: 10}
	h := newTestHandler(repo)

	manifest := &firmwarepb.DeviceManifest{
		DeviceId: "leaflab-aabbccdd",
		Sensors: []*firmwarepb.SensorDescriptor{
			{
				Name:       "light",
				Type:       firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE,
				Unit:       "lx",
				I2CAddress: 0x23,
				MuxAddress: 0x70,
				MuxChannel: 1,
			},
		},
	}

	if err := h.handleManifest(context.Background(), manifest.DeviceId, marshalManifest(t, manifest)); err != nil {
		t.Fatalf("handleManifest: %v", err)
	}

	if len(repo.upsertSensorCalls) != 1 {
		t.Fatalf("expected 1 UpsertSensor call, got %d", len(repo.upsertSensorCalls))
	}
	call := repo.upsertSensorCalls[0]

	if call.hw == nil {
		t.Fatal("expected non-nil HardwareAddress, got nil")
	}
	if !call.hw.I2CAddress.Equal(hwkey.Address(0x23)) {
		t.Errorf("I2CAddress: want 0x23, got %s", call.hw.I2CAddress)
	}
	if len(call.hw.MuxPath) != 1 {
		t.Fatalf("MuxPath: want 1 hop, got %d", len(call.hw.MuxPath))
	}
	if call.hw.MuxPath[0].MuxAddress != 0x70 {
		t.Errorf("MuxPath[0].MuxAddress: want 0x70, got 0x%x", call.hw.MuxPath[0].MuxAddress)
	}
	if call.hw.MuxPath[0].MuxChannel != 1 {
		t.Errorf("MuxPath[0].MuxChannel: want 1, got %d", call.hw.MuxPath[0].MuxChannel)
	}
	if call.name != "light" {
		t.Errorf("name: want %q, got %q", "light", call.name)
	}
}

// TestHandleManifest_NoHWAddressUsesNameFallback verifies that a SensorDescriptor
// with i2c_address == 0 results in hw == nil (name-based upsert path).
func TestHandleManifest_NoHWAddressUsesNameFallback(t *testing.T) {
	repo := &stubRepo{boardID: 1, sensorTypeID: 2, sensorID: 10}
	h := newTestHandler(repo)

	manifest := &firmwarepb.DeviceManifest{
		DeviceId: "leaflab-aabbccdd",
		Sensors: []*firmwarepb.SensorDescriptor{
			{
				Name: "temp",
				Type: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE,
				Unit: "°C",
				// I2CAddress == 0: no hardware address
			},
		},
	}

	if err := h.handleManifest(context.Background(), manifest.DeviceId, marshalManifest(t, manifest)); err != nil {
		t.Fatalf("handleManifest: %v", err)
	}

	if len(repo.upsertSensorCalls) != 1 {
		t.Fatalf("expected 1 UpsertSensor call, got %d", len(repo.upsertSensorCalls))
	}
	call := repo.upsertSensorCalls[0]

	if call.hw != nil {
		t.Errorf("expected nil HardwareAddress (name fallback path), got %+v", call.hw)
	}
	if call.name != "temp" {
		t.Errorf("name: want %q, got %q", "temp", call.name)
	}
}

// TestHandleManifest_HWHistoryReceivesAddress verifies that handleManifest
// passes the same *HardwareAddress it builds for UpsertSensor through to
// UpsertSensorHWHistory (FR16.1) -- including the sensor_id UpsertSensor
// returned, not some other value.
func TestHandleManifest_HWHistoryReceivesAddress(t *testing.T) {
	repo := &stubRepo{boardID: 1, sensorTypeID: 2, sensorID: 77}
	h := newTestHandler(repo)

	manifest := &firmwarepb.DeviceManifest{
		DeviceId: "leaflab-aabbccdd",
		Sensors: []*firmwarepb.SensorDescriptor{
			{
				Name:       "light",
				Type:       firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE,
				Unit:       "lx",
				I2CAddress: 0x23,
				MuxAddress: 0x70,
				MuxChannel: 1,
			},
		},
	}

	if err := h.handleManifest(context.Background(), manifest.DeviceId, marshalManifest(t, manifest)); err != nil {
		t.Fatalf("handleManifest: %v", err)
	}

	if len(repo.upsertSensorHWHistoryCalls) != 1 {
		t.Fatalf("expected 1 UpsertSensorHWHistory call, got %d", len(repo.upsertSensorHWHistoryCalls))
	}
	call := repo.upsertSensorHWHistoryCalls[0]
	if call.sensorID != 77 {
		t.Errorf("UpsertSensorHWHistory sensorID: want 77, got %d", call.sensorID)
	}
	if call.hw == nil {
		t.Fatal("expected non-nil HardwareAddress, got nil")
	}
	if !call.hw.I2CAddress.Equal(hwkey.Address(0x23)) {
		t.Errorf("I2CAddress: want 0x23, got %s", call.hw.I2CAddress)
	}
	if len(call.hw.MuxPath) != 1 || call.hw.MuxPath[0].MuxAddress != 0x70 || call.hw.MuxPath[0].MuxChannel != 1 {
		t.Errorf("MuxPath: want [{0x70 1}], got %+v", call.hw.MuxPath)
	}
}

// TestHandleManifest_MultipleSensors verifies hw address extraction across a
// mixed manifest (some sensors on mux, some not).
func TestHandleManifest_MultipleSensors(t *testing.T) {
	repo := &stubRepo{boardID: 1, sensorTypeID: 2, sensorID: 10}
	h := newTestHandler(repo)

	manifest := &firmwarepb.DeviceManifest{
		DeviceId: "leaflab-aabbccdd",
		Sensors: []*firmwarepb.SensorDescriptor{
			{Name: "light", Type: firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE, Unit: "lx", I2CAddress: 0x23, MuxAddress: 0x70, MuxChannel: 1},
			{Name: "temp", Type: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE, Unit: "°C", I2CAddress: 0x44, MuxAddress: 0x70, MuxChannel: 0},
			{Name: "legacy", Type: firmwarepb.SensorType_SENSOR_TYPE_UNKNOWN, Unit: ""},
		},
	}

	if err := h.handleManifest(context.Background(), manifest.DeviceId, marshalManifest(t, manifest)); err != nil {
		t.Fatalf("handleManifest: %v", err)
	}

	if len(repo.upsertSensorCalls) != 3 {
		t.Fatalf("expected 3 UpsertSensor calls, got %d", len(repo.upsertSensorCalls))
	}

	lightCall := repo.upsertSensorCalls[0]
	if lightCall.hw == nil || !lightCall.hw.I2CAddress.Equal(hwkey.Address(0x23)) {
		t.Errorf("light sensor hw address wrong: %+v", lightCall.hw)
	}
	if len(lightCall.hw.MuxPath) != 1 || lightCall.hw.MuxPath[0].MuxAddress != 0x70 || lightCall.hw.MuxPath[0].MuxChannel != 1 {
		t.Errorf("light sensor mux path wrong: %+v", lightCall.hw.MuxPath)
	}

	tempCall := repo.upsertSensorCalls[1]
	if tempCall.hw == nil || !tempCall.hw.I2CAddress.Equal(hwkey.Address(0x44)) {
		t.Errorf("temp sensor hw address wrong: %+v", tempCall.hw)
	}
	// MuxAddress 0x70, channel 0 is a valid mux position (SD0).
	if len(tempCall.hw.MuxPath) != 1 || tempCall.hw.MuxPath[0].MuxAddress != 0x70 || tempCall.hw.MuxPath[0].MuxChannel != 0 {
		t.Errorf("temp sensor mux path wrong: %+v", tempCall.hw.MuxPath)
	}

	legacyCall := repo.upsertSensorCalls[2]
	if legacyCall.hw != nil {
		t.Errorf("legacy sensor: expected nil hw, got %+v", legacyCall.hw)
	}
}

// TestHandleManifest_CachePopulated verifies the in-memory cache is set after
// a successful manifest.
func TestHandleManifest_CachePopulated(t *testing.T) {
	repo := &stubRepo{boardID: 1, sensorTypeID: 2, sensorID: 42}
	h := newTestHandler(repo)

	manifest := &firmwarepb.DeviceManifest{
		DeviceId: "leaflab-aabbccdd",
		Sensors: []*firmwarepb.SensorDescriptor{
			{Name: "eco2", Type: firmwarepb.SensorType_SENSOR_TYPE_ECO2, Unit: "ppm", I2CAddress: 0x5A, MuxAddress: 0x70, MuxChannel: 2},
		},
	}

	if err := h.handleManifest(context.Background(), manifest.DeviceId, marshalManifest(t, manifest)); err != nil {
		t.Fatalf("handleManifest: %v", err)
	}

	info, ok := h.cache.Get("leaflab-aabbccdd", "eco2")
	if !ok {
		t.Fatal("sensor not in cache after manifest")
	}
	if info.SensorID != 42 {
		t.Errorf("cache SensorID: want 42, got %d", info.SensorID)
	}
}

// TestHandleManifest_MuxPathSingleHop verifies that a sensor with a non-zero
// MuxAddress produces exactly one MuxHop in HardwareAddress.MuxPath.
func TestHandleManifest_MuxPathSingleHop(t *testing.T) {
	repo := &stubRepo{boardID: 1, sensorTypeID: 2, sensorID: 10}
	h := newTestHandler(repo)

	manifest := &firmwarepb.DeviceManifest{
		DeviceId: "leaflab-aabbccdd",
		Sensors: []*firmwarepb.SensorDescriptor{
			{
				Name:       "light",
				Type:       firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE,
				Unit:       "lx",
				I2CAddress: 0x23,
				MuxAddress: 0x70,
				MuxChannel: 6,
			},
		},
	}

	if err := h.handleManifest(context.Background(), manifest.DeviceId, marshalManifest(t, manifest)); err != nil {
		t.Fatalf("handleManifest: %v", err)
	}

	call := repo.upsertSensorCalls[0]
	if call.hw == nil {
		t.Fatal("expected non-nil HardwareAddress")
	}
	if len(call.hw.MuxPath) != 1 {
		t.Fatalf("expected 1 mux hop, got %d", len(call.hw.MuxPath))
	}
	if call.hw.MuxPath[0].MuxAddress != 0x70 {
		t.Errorf("MuxPath[0].MuxAddress: want 0x70, got 0x%x", call.hw.MuxPath[0].MuxAddress)
	}
	if call.hw.MuxPath[0].MuxChannel != 6 {
		t.Errorf("MuxPath[0].MuxChannel: want 6, got %d", call.hw.MuxPath[0].MuxChannel)
	}
}

// TestHandleManifest_DirectSensorEmptyMuxPath verifies that a sensor with no
// mux (MuxAddress == 0) produces an empty MuxPath, not nil.
func TestHandleManifest_DirectSensorEmptyMuxPath(t *testing.T) {
	repo := &stubRepo{boardID: 1, sensorTypeID: 2, sensorID: 10}
	h := newTestHandler(repo)

	manifest := &firmwarepb.DeviceManifest{
		DeviceId: "leaflab-aabbccdd",
		Sensors: []*firmwarepb.SensorDescriptor{
			{
				Name:       "light",
				Type:       firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE,
				Unit:       "lx",
				I2CAddress: 0x23,
				// MuxAddress == 0: direct on root bus
			},
		},
	}

	if err := h.handleManifest(context.Background(), manifest.DeviceId, marshalManifest(t, manifest)); err != nil {
		t.Fatalf("handleManifest: %v", err)
	}

	call := repo.upsertSensorCalls[0]
	if call.hw == nil {
		t.Fatal("expected non-nil HardwareAddress for sensor with i2c_address > 0")
	}
	if len(call.hw.MuxPath) != 0 {
		t.Errorf("expected empty MuxPath for direct sensor, got %v", call.hw.MuxPath)
	}
}

// TestHandleConfigAck_AcceptedCallsApplyRegionsAndSetsCache verifies that an
// accepted DeviceConfigAck triggers ApplyConfigRegions and updates the config
// version cache; a rejected ack does neither.
func TestHandleConfigAck_AcceptedCallsApplyRegionsAndSetsCache(t *testing.T) {
	repo := &stubRepo{boardID: 7}
	h := newTestHandler(repo)

	ack := &configpb.DeviceConfigAck{
		DeviceId:       "leaflab-aabbccdd",
		AppliedVersion: 3,
		Accepted:       true,
	}
	body, err := proto.Marshal(ack)
	if err != nil {
		t.Fatalf("marshal ack: %v", err)
	}

	if err := h.handleConfigAck(context.Background(), "leaflab-aabbccdd", body); err != nil {
		t.Fatalf("handleConfigAck: %v", err)
	}

	if len(repo.applyConfigRegionsCalls) != 1 {
		t.Fatalf("expected 1 ApplyConfigRegions call, got %d", len(repo.applyConfigRegionsCalls))
	}
	call := repo.applyConfigRegionsCalls[0]
	if call.boardID != 7 {
		t.Errorf("ApplyConfigRegions boardID: want 7, got %d", call.boardID)
	}
	if call.version != 3 {
		t.Errorf("ApplyConfigRegions version: want 3, got %d", call.version)
	}

	v, ok := h.cache.GetConfigVersion("leaflab-aabbccdd")
	if !ok {
		t.Fatal("config version not set in cache after accepted ack")
	}
	if v != 3 {
		t.Errorf("cache config version: want 3, got %d", v)
	}
}

// TestHandleManifest_FR16_3_SimultaneousRewireAndRename_SameSensorID covers
// FR16.3: a manifest entry whose address *and* name both change in the same
// message must resolve, by elimination, to the one existing sensor identity
// nothing else in the manifest claims -- routed through
// RewireAndRenameSensor, not UpsertSensor, so it never mints a second
// sensor row for the same physical sensor.
func TestHandleManifest_FR16_3_SimultaneousRewireAndRename_SameSensorID(t *testing.T) {
	existing := BoardSensorIdentity{
		SensorID:     55,
		Name:         "old_light",
		SensorTypeID: 2,
		HW: &HardwareAddress{
			I2CAddress: hwkey.Address(0x23),
			MuxPath:    hwkey.MuxPath{{MuxAddress: 0x70, MuxChannel: 1}},
		},
	}
	repo := &stubRepo{
		boardID:               1,
		sensorTypeID:          2,
		sensorID:              999, // must never be used: this entry must not go through UpsertSensor
		boardSensorIdentities: []BoardSensorIdentity{existing},
	}
	h := newTestHandler(repo)

	manifest := &firmwarepb.DeviceManifest{
		DeviceId: "leaflab-aabbccdd",
		Sensors: []*firmwarepb.SensorDescriptor{
			{
				Name:       "new_light", // renamed
				Type:       firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE,
				Unit:       "lx",
				I2CAddress: 0x44, // rewired
				MuxAddress: 0x71,
				MuxChannel: 2,
			},
		},
	}

	if err := h.handleManifest(context.Background(), manifest.DeviceId, marshalManifest(t, manifest)); err != nil {
		t.Fatalf("handleManifest: %v", err)
	}

	if len(repo.upsertSensorCalls) != 0 {
		t.Fatalf("expected 0 UpsertSensor calls (must not mint a second sensor row), got %d", len(repo.upsertSensorCalls))
	}
	if len(repo.rewireAndRenameSensorCalls) != 1 {
		t.Fatalf("expected 1 RewireAndRenameSensor call, got %d", len(repo.rewireAndRenameSensorCalls))
	}
	call := repo.rewireAndRenameSensorCalls[0]
	if call.sensorID != existing.SensorID {
		t.Errorf("RewireAndRenameSensor sensorID = %d, want unchanged %d (same sensor_id, FR16.3)", call.sensorID, existing.SensorID)
	}
	if call.name != "new_light" {
		t.Errorf("RewireAndRenameSensor name = %q, want %q", call.name, "new_light")
	}
	if call.hw == nil || !call.hw.I2CAddress.Equal(hwkey.Address(0x44)) {
		t.Errorf("RewireAndRenameSensor hw = %+v, want i2c_address 0x44", call.hw)
	}

	if len(repo.upsertSensorHWHistoryCalls) != 1 || repo.upsertSensorHWHistoryCalls[0].sensorID != existing.SensorID {
		t.Fatalf("UpsertSensorHWHistory calls = %+v, want exactly one for sensor_id %d", repo.upsertSensorHWHistoryCalls, existing.SensorID)
	}

	info, ok := h.cache.Get("leaflab-aabbccdd", "new_light")
	if !ok {
		t.Fatal("sensor not in cache under its new name after simultaneous rewire+rename")
	}
	if info.SensorID != existing.SensorID {
		t.Errorf("cache SensorID = %d, want unchanged %d", info.SensorID, existing.SensorID)
	}
}

// TestHandleManifest_RewireStableName_FallsThroughToUpsertSensor is a
// regression guard for FR16 case 2 (today's `ON CONFLICT` path): a manifest
// entry whose name is unchanged but whose hardware address changed must
// still match by name during resolveManifestIdentities's elimination pass,
// so it is *not* treated as FR16.3's simultaneous-change case -- it falls
// through to UpsertSensor exactly as before this feature existed.
func TestHandleManifest_RewireStableName_FallsThroughToUpsertSensor(t *testing.T) {
	existing := BoardSensorIdentity{
		SensorID:     10,
		Name:         "temp",
		SensorTypeID: 2,
		HW:           &HardwareAddress{I2CAddress: hwkey.Address(0x23)},
	}
	repo := &stubRepo{
		boardID:               1,
		sensorTypeID:          2,
		sensorID:              10,
		boardSensorIdentities: []BoardSensorIdentity{existing},
	}
	h := newTestHandler(repo)

	manifest := &firmwarepb.DeviceManifest{
		DeviceId: "leaflab-aabbccdd",
		Sensors: []*firmwarepb.SensorDescriptor{
			{Name: "temp", Type: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE, Unit: "°C", I2CAddress: 0x44},
		},
	}

	if err := h.handleManifest(context.Background(), manifest.DeviceId, marshalManifest(t, manifest)); err != nil {
		t.Fatalf("handleManifest: %v", err)
	}

	if len(repo.rewireAndRenameSensorCalls) != 0 {
		t.Fatalf("expected 0 RewireAndRenameSensor calls (name-stable rewire is case 2, not FR16.3's elimination case), got %d", len(repo.rewireAndRenameSensorCalls))
	}
	if len(repo.upsertSensorCalls) != 1 {
		t.Fatalf("expected 1 UpsertSensor call, got %d", len(repo.upsertSensorCalls))
	}
}

// TestHandleManifest_RenameStableKey_FallsThroughToUpsertSensor is a
// regression guard for FR16 case 1: a manifest entry whose hardware address
// is unchanged but whose name changed must still match by hardware key
// during elimination, so it also is not treated as FR16.3's case -- it
// falls through to UpsertSensor, whose own hw-address lookup (not
// elimination) resolves it.
func TestHandleManifest_RenameStableKey_FallsThroughToUpsertSensor(t *testing.T) {
	existing := BoardSensorIdentity{
		SensorID:     20,
		Name:         "old_name",
		SensorTypeID: 2,
		HW: &HardwareAddress{
			I2CAddress: hwkey.Address(0x23),
			MuxPath:    hwkey.MuxPath{{MuxAddress: 0x70, MuxChannel: 1}},
		},
	}
	repo := &stubRepo{
		boardID:               1,
		sensorTypeID:          2,
		sensorID:              20,
		boardSensorIdentities: []BoardSensorIdentity{existing},
	}
	h := newTestHandler(repo)

	manifest := &firmwarepb.DeviceManifest{
		DeviceId: "leaflab-aabbccdd",
		Sensors: []*firmwarepb.SensorDescriptor{
			{
				Name:       "new_name", // renamed
				Type:       firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE,
				Unit:       "lx",
				I2CAddress: 0x23, // unchanged
				MuxAddress: 0x70,
				MuxChannel: 1,
			},
		},
	}

	if err := h.handleManifest(context.Background(), manifest.DeviceId, marshalManifest(t, manifest)); err != nil {
		t.Fatalf("handleManifest: %v", err)
	}

	if len(repo.rewireAndRenameSensorCalls) != 0 {
		t.Fatalf("expected 0 RewireAndRenameSensor calls (key-stable rename is case 1, not FR16.3's elimination case), got %d", len(repo.rewireAndRenameSensorCalls))
	}
	if len(repo.upsertSensorCalls) != 1 {
		t.Fatalf("expected 1 UpsertSensor call, got %d", len(repo.upsertSensorCalls))
	}
}

// TestHandleManifest_AmbiguousElimination_FallsThroughToUpsertSensor checks
// resolveManifestIdentities's own safety valve: when elimination would be
// ambiguous (more than one unresolved entry chasing one unclaimed identity),
// it resolves nothing, and every entry falls through to UpsertSensor's own
// (correct, case-3) resolution instead of guessing which entry gets the
// existing identity.
func TestHandleManifest_AmbiguousElimination_FallsThroughToUpsertSensor(t *testing.T) {
	existing := BoardSensorIdentity{
		SensorID:     30,
		Name:         "a",
		SensorTypeID: 2,
		HW:           &HardwareAddress{I2CAddress: hwkey.Address(0x23)},
	}
	repo := &stubRepo{
		boardID:               1,
		sensorTypeID:          2,
		sensorID:              40,
		boardSensorIdentities: []BoardSensorIdentity{existing},
	}
	h := newTestHandler(repo)

	manifest := &firmwarepb.DeviceManifest{
		DeviceId: "leaflab-aabbccdd",
		Sensors: []*firmwarepb.SensorDescriptor{
			{Name: "b", Type: firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE, Unit: "lx", I2CAddress: 0x55},
			{Name: "c", Type: firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE, Unit: "lx", I2CAddress: 0x66},
		},
	}

	if err := h.handleManifest(context.Background(), manifest.DeviceId, marshalManifest(t, manifest)); err != nil {
		t.Fatalf("handleManifest: %v", err)
	}

	if len(repo.rewireAndRenameSensorCalls) != 0 {
		t.Fatalf("expected 0 RewireAndRenameSensor calls (2 unresolved entries vs 1 unclaimed identity is ambiguous), got %d", len(repo.rewireAndRenameSensorCalls))
	}
	if len(repo.upsertSensorCalls) != 2 {
		t.Fatalf("expected 2 UpsertSensor calls (both entries fall through as genuinely new), got %d", len(repo.upsertSensorCalls))
	}
}

// TestResolveManifestIdentities is a direct table-driven test of the pure
// elimination function underlying the handler-level tests above, pinning
// down edge cases (a sensor_type mismatch preventing an otherwise-eligible
// pairing) that are awkward to drive through handleManifest with stubRepo's
// fixed sensorTypeID.
func TestResolveManifestIdentities(t *testing.T) {
	hwA := &HardwareAddress{I2CAddress: hwkey.Address(0x23)}
	hwB := &HardwareAddress{I2CAddress: hwkey.Address(0x44)}

	tests := []struct {
		name          string
		existing      []BoardSensorIdentity
		sensorTypeIDs []int64
		hws           []*HardwareAddress
		names         []string
		want          map[int]int64
	}{
		{
			name: "simultaneous address and name change resolves by elimination",
			existing: []BoardSensorIdentity{
				{SensorID: 1, Name: "old", SensorTypeID: 2, HW: hwA},
			},
			sensorTypeIDs: []int64{2},
			hws:           []*HardwareAddress{hwB},
			names:         []string{"new"},
			want:          map[int]int64{0: 1},
		},
		{
			name: "name match alone is not eliminated (case 2 handles it)",
			existing: []BoardSensorIdentity{
				{SensorID: 1, Name: "temp", SensorTypeID: 2, HW: hwA},
			},
			sensorTypeIDs: []int64{2},
			hws:           []*HardwareAddress{hwB},
			names:         []string{"temp"},
			want:          map[int]int64{},
		},
		{
			name: "hw match alone is not eliminated (case 1 handles it)",
			existing: []BoardSensorIdentity{
				{SensorID: 1, Name: "old", SensorTypeID: 2, HW: hwA},
			},
			sensorTypeIDs: []int64{2},
			hws:           []*HardwareAddress{hwA},
			names:         []string{"new"},
			want:          map[int]int64{},
		},
		{
			name: "ambiguous: two unresolved entries, one unclaimed identity",
			existing: []BoardSensorIdentity{
				{SensorID: 1, Name: "a", SensorTypeID: 2, HW: hwA},
			},
			sensorTypeIDs: []int64{2, 2},
			hws:           []*HardwareAddress{hwB, hwB},
			names:         []string{"b", "c"},
			want:          map[int]int64{},
		},
		{
			name: "sensor_type mismatch prevents an otherwise-eligible pairing",
			existing: []BoardSensorIdentity{
				{SensorID: 1, Name: "old", SensorTypeID: 2, HW: hwA},
			},
			sensorTypeIDs: []int64{5}, // different sensor_type than the unclaimed identity
			hws:           []*HardwareAddress{hwB},
			names:         []string{"new"},
			want:          map[int]int64{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveManifestIdentities(tt.existing, tt.sensorTypeIDs, tt.hws, tt.names)
			if len(got) != len(tt.want) {
				t.Fatalf("resolveManifestIdentities = %+v, want %+v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("resolveManifestIdentities[%d] = %d, want %d", k, got[k], v)
				}
			}
		})
	}
}

// TestHandleConfigAck_RejectedSkipsApplyRegions verifies that a rejected ack
// does not call ApplyConfigRegions and does not update the config version cache.
func TestHandleConfigAck_RejectedSkipsApplyRegions(t *testing.T) {
	repo := &stubRepo{boardID: 7}
	h := newTestHandler(repo)

	ack := &configpb.DeviceConfigAck{
		DeviceId:       "leaflab-aabbccdd",
		AppliedVersion: 2,
		Accepted:       false,
		Reason:         "stale_version",
	}
	body, err := proto.Marshal(ack)
	if err != nil {
		t.Fatalf("marshal ack: %v", err)
	}

	if err := h.handleConfigAck(context.Background(), "leaflab-aabbccdd", body); err != nil {
		t.Fatalf("handleConfigAck: %v", err)
	}

	if len(repo.applyConfigRegionsCalls) != 0 {
		t.Errorf("expected 0 ApplyConfigRegions calls on rejection, got %d", len(repo.applyConfigRegionsCalls))
	}

	if _, ok := h.cache.GetConfigVersion("leaflab-aabbccdd"); ok {
		t.Error("config version should not be set in cache after rejected ack")
	}
}

// --- FR73: cross-process cache invalidation (ApplyInvalidation, SensorCache
// eviction, and the bounded staleness backstop) ---
//
// The cross-process broadcast itself (a real RabbitMQ fanout exchange, the
// every-replica property, publisher/subscriber reconnect after a connection
// drop) needs a real broker and is covered by
// leaflab/processor/invalidation_integration_test.go instead -- see that
// file's doc comment. The tests below cover the pure decision logic every
// process runs once it has received (or, for the backstop, failed to
// receive) an event: which cache key to evict, and that ReplaceAll's full
// replace (not Load's additive merge) is what makes a dropped rename event
// self-heal instead of leaving an orphan forever.

func int64ptr(v int64) *int64 { return &v }

// TestApplyInvalidation_RegionOrIdentityEvictsObservedKey verifies that a
// region or identity invalidation.Event evicts the cache entry under the
// key the event itself carries (DeviceID/SensorName) -- the common case,
// contrasted with the rename case below where the key to evict is *not*
// the one the event's own SensorName field names.
func TestApplyInvalidation_RegionOrIdentityEvictsObservedKey(t *testing.T) {
	for _, kind := range []invalidation.Kind{invalidation.KindRegion, invalidation.KindIdentity} {
		t.Run(string(kind), func(t *testing.T) {
			cache := NewSensorCache()
			cache.Set("leaflab-aabbccdd", "temp", SensorInfo{SensorID: 1, RegionID: int64ptr(10)})

			ApplyInvalidation(cache, invalidation.Event{
				Kind:       kind,
				DeviceID:   "leaflab-aabbccdd",
				SensorName: "temp",
			})

			if _, ok := cache.Get("leaflab-aabbccdd", "temp"); ok {
				t.Fatalf("expected %s invalidation to evict the cache entry, but it is still present", kind)
			}
		})
	}
}

// TestApplyInvalidation_RenameEvictsPriorNameOnly is the load-bearing
// rename-case test: the cache is keyed device_id -> sensor_name, so a
// rename's *new* name (Event.SensorName) was never a cache key to begin
// with -- what must be evicted is Event.PriorSensorName, or the entry under
// the old name is an orphan nothing else ever touches (see
// SensorCache.Invalidate's doc comment). It also asserts the new-name entry
// (however it got there -- a concurrent handleManifest, or a prior
// cache-miss DB lookup) is left untouched by this event.
func TestApplyInvalidation_RenameEvictsPriorNameOnly(t *testing.T) {
	cache := NewSensorCache()
	cache.Set("leaflab-aabbccdd", "old_light", SensorInfo{SensorID: 5})
	cache.Set("leaflab-aabbccdd", "new_light", SensorInfo{SensorID: 5})

	ApplyInvalidation(cache, invalidation.Event{
		Kind:            invalidation.KindName,
		DeviceID:        "leaflab-aabbccdd",
		SensorName:      "new_light",
		PriorSensorName: "old_light",
	})

	if _, ok := cache.Get("leaflab-aabbccdd", "old_light"); ok {
		t.Fatal("expected rename invalidation to evict the prior-name entry, but it left a stale orphan")
	}
	if info, ok := cache.Get("leaflab-aabbccdd", "new_light"); !ok || info.SensorID != 5 {
		t.Fatalf("rename invalidation must not evict/alter the new-name entry, got %+v (ok=%v)", info, ok)
	}
}

// TestSensorCache_ReplaceAll_ClearsOrphanedPriorNameEntry is the backstop's
// load-bearing test: it reproduces the exact state a *dropped* rename
// invalidation.Event leaves behind (an orphaned entry under the sensor's
// prior name -- see TestApplyInvalidation_RenameEvictsPriorNameOnly above
// for the event that would normally have cleared it) and asserts that
// ReplaceAll, given only current DB state (which has no reason to ever
// mention the prior name), evicts that orphan. This is precisely the
// property Load (an additive merge, used only for the one-time startup
// pre-warm) does not have -- see the contrasting test below.
func TestSensorCache_ReplaceAll_ClearsOrphanedPriorNameEntry(t *testing.T) {
	cache := NewSensorCache()
	// Simulates the cache's state immediately after a rename whose
	// invalidation.Event never arrived (e.g. lost during a RabbitMQ
	// reconnect window -- see leaflab/invalidation/subscriber.go's Start
	// doc comment): the entry under the sensor's prior name is still here,
	// because nothing ever told this process to evict it.
	cache.Set("leaflab-aabbccdd", "old_light", SensorInfo{SensorID: 5})

	// A backstop reload snapshots current DB state, which -- after the
	// rename actually committed -- only has the sensor under its new name.
	cache.ReplaceAll(map[string]map[string]SensorInfo{
		"leaflab-aabbccdd": {
			"new_light": {SensorID: 5},
		},
	})

	if _, ok := cache.Get("leaflab-aabbccdd", "old_light"); ok {
		t.Fatal("ReplaceAll must fully replace the cached set, but the orphaned prior-name entry survived")
	}
	if info, ok := cache.Get("leaflab-aabbccdd", "new_light"); !ok || info.SensorID != 5 {
		t.Fatalf("expected new_light -> SensorID 5 after ReplaceAll, got %+v (ok=%v)", info, ok)
	}
}

// TestSensorCache_Load_DoesNotClearOrphanedPriorNameEntry contrasts the
// previous test: Load is an additive merge (by design -- see its doc
// comment, it's used only for the one-time startup pre-warm and must not
// clobber entries a concurrent handleManifest just wrote). If
// RunCacheBackstop ever regressed to calling Load instead of ReplaceAll, a
// dropped rename event's orphan would survive every backstop cycle forever
// -- this test pins down why that regression would be silent without a
// test like this one.
func TestSensorCache_Load_DoesNotClearOrphanedPriorNameEntry(t *testing.T) {
	cache := NewSensorCache()
	cache.Set("leaflab-aabbccdd", "old_light", SensorInfo{SensorID: 5})

	cache.Load(map[string]map[string]SensorInfo{
		"leaflab-aabbccdd": {
			"new_light": {SensorID: 5},
		},
	})

	if _, ok := cache.Get("leaflab-aabbccdd", "old_light"); !ok {
		t.Fatal("expected Load's additive merge to leave the orphaned prior-name entry in place (contrast with ReplaceAll)")
	}
}

// fakeCacheLoader is a SensorCacheLoader test double for RunCacheBackstop /
// reloadCache that returns a fixed snapshot, standing in for a database
// read.
type fakeCacheLoader struct {
	sensors  map[string]map[string]SensorInfo
	versions map[string]int64
}

func (f *fakeCacheLoader) LoadSensorCache(_ context.Context) (map[string]map[string]SensorInfo, error) {
	return f.sensors, nil
}

func (f *fakeCacheLoader) LoadConfigVersionCache(_ context.Context) (map[string]int64, error) {
	return f.versions, nil
}

// TestReloadCache_SelfHealsDroppedRenameEvent exercises reloadCache (the
// unit RunCacheBackstop calls on each tick) directly against the dropped-
// rename scenario, and records how long the reload itself took -- the
// per-cycle cost that, multiplied by however many ticks a paused Subscriber
// misses, determines how long a dropped event stays wrong for (bounded by
// Config.CacheBackstopInterval, not by this reload call).
func TestReloadCache_SelfHealsDroppedRenameEvent(t *testing.T) {
	cache := NewSensorCache()
	cache.Set("leaflab-aabbccdd", "old_light", SensorInfo{SensorID: 5})

	loader := &fakeCacheLoader{
		sensors: map[string]map[string]SensorInfo{
			"leaflab-aabbccdd": {"new_light": {SensorID: 5}},
		},
		versions: map[string]int64{"leaflab-aabbccdd": 1},
	}

	start := time.Now()
	reloadCache(context.Background(), loader, cache, nil)
	elapsed := time.Since(start)
	t.Logf("backstop reload restored correctness in %s", elapsed)

	if _, ok := cache.Get("leaflab-aabbccdd", "old_light"); ok {
		t.Fatal("backstop reload did not clear the orphaned prior-name entry left by the dropped rename event")
	}
	if _, ok := cache.Get("leaflab-aabbccdd", "new_light"); !ok {
		t.Fatal("backstop reload did not restore the current entry")
	}
	if v, ok := cache.GetConfigVersion("leaflab-aabbccdd"); !ok || v != 1 {
		t.Fatalf("backstop reload did not restore the config version cache, got %d (ok=%v)", v, ok)
	}
}

// TestRunCacheBackstop_SelfHealsWithinInterval runs the real ticker-driven
// RunCacheBackstop loop (not just one reloadCache call) against a short
// interval, simulating a Subscriber "paused past the bound" (see this
// package's BUILD.bazel doc comment and the issue's Testing criteria): no
// invalidation.Event is ever delivered here, so the cache would stay wrong
// forever without RunCacheBackstop running in the background. It records
// how long self-healing actually took.
func TestRunCacheBackstop_SelfHealsWithinInterval(t *testing.T) {
	cache := NewSensorCache()
	cache.Set("leaflab-aabbccdd", "old_light", SensorInfo{SensorID: 5})

	loader := &fakeCacheLoader{
		sensors: map[string]map[string]SensorInfo{
			"leaflab-aabbccdd": {"new_light": {SensorID: 5}},
		},
	}

	const backstopInterval = 20 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	go RunCacheBackstop(ctx, backstopInterval, loader, cache, nil)

	deadline := time.NewTimer(1 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, hasNew := cache.Get("leaflab-aabbccdd", "new_light")
		_, hasOld := cache.Get("leaflab-aabbccdd", "old_light")
		if hasNew && !hasOld {
			break
		}
		select {
		case <-deadline.C:
			t.Fatalf("backstop did not self-heal the dropped rename event within 1s (backstop interval %s)", backstopInterval)
		case <-ticker.C:
		}
	}
	t.Logf("backstop self-healed the dropped rename event in %s (backstop interval %s)", time.Since(start), backstopInterval)
}

// --- FR73: handleSensorReading's cache-miss/invalidation-driven re-read
// path, exercised end-to-end through the handler itself ---
//
// The tests above (ApplyInvalidation, SensorCache eviction, ReplaceAll,
// reloadCache, RunCacheBackstop) each pin down one piece of the decision
// logic in isolation. None of them ever call handleSensorReading, so none
// of them prove the pieces actually compose: that an evicted cache entry
// really does fall through to stubRepo.GetSensor, that the value GetSensor
// returns is really what gets stamped onto the reading via InsertReading,
// and that the cache is repopulated with it. The tests below close that
// gap using getSensorResults/insertReadingCalls above.

// TestHandleSensorReading_CrossProcessRegionChange_CacheMissRereadsNewRegion
// is the load-bearing test FR73's Testing section names explicitly: a
// region assignment commits through some other writer -- this test doesn't
// care which; see the two tests below for the API's and the processor's own
// two writer surfaces specifically -- while this process has the sensor
// cached under its old region. Once the cache entry is invalidated (as
// main.go's Subscriber does on receiving the cross-process event; the
// broker's every-replica delivery of that event is proven separately by
// invalidation_integration_test.go's TestInvalidation_EveryReplicaFanout),
// the very next reading must fall through to repo.GetSensor -- not keep
// serving the stale cached RegionID -- and the reading must be stamped with
// the *new* region. No manifest and no config push occurs anywhere in this
// test.
func TestHandleSensorReading_CrossProcessRegionChange_CacheMissRereadsNewRegion(t *testing.T) {
	const deviceID = "leaflab-crossproc"
	const sensorName = "temp"
	oldRegion := int64ptr(1)
	newRegion := int64ptr(2)

	repo := &stubRepo{
		getSensorResults: map[string]SensorInfo{
			deviceID + "/" + sensorName: {SensorID: 7, RegionID: newRegion},
		},
	}
	h := newTestHandler(repo)

	// "the processor running ... with a sensor cached": pre-populate the
	// cache exactly as handleManifest would have, under the *old* region.
	h.cache.Set(deviceID, sensorName, SensorInfo{SensorID: 7, RegionID: oldRegion})

	// "region assignment happens via a second process": simulated here by
	// the fact that stubRepo.GetSensor above already reflects the new
	// region -- some other writer (the API, or this same process's own
	// ApplyConfigRegions, see the tests below) has already committed it.
	// The signal that a second process's Subscriber -- here, this one --
	// received: evict the stale cache entry.
	ApplyInvalidation(h.cache, invalidation.Event{
		Kind:       invalidation.KindRegion,
		DeviceID:   deviceID,
		SensorName: sensorName,
	})

	reading := &firmwarepb.SensorReading{Value: 21.5, UptimeMs: 60000}
	body, err := proto.Marshal(reading)
	if err != nil {
		t.Fatalf("marshal reading: %v", err)
	}
	// "a reading arrives > 5s later": the bound itself is a real-time
	// property of the signal (proven by invalidation_integration_test.go
	// against a real broker); what this test proves is that *whenever* the
	// reading arrives after invalidation, it is never served the stale
	// value -- there is no time-based fallback to the old cache entry.
	if err := h.handleSensorReading(context.Background(), deviceID, sensorName, body); err != nil {
		t.Fatalf("handleSensorReading: %v", err)
	}

	if len(repo.insertReadingCalls) != 1 {
		t.Fatalf("expected 1 InsertReading call, got %d", len(repo.insertReadingCalls))
	}
	call := repo.insertReadingCalls[0]
	if call.sensorID != 7 {
		t.Errorf("InsertReading sensorID = %d, want 7", call.sensorID)
	}
	if call.regionID == nil || *call.regionID != *newRegion {
		t.Fatalf("sensor_reading.region_id = %v, want the NEW region %d -- a cached view outlived the fact it caches", call.regionID, *newRegion)
	}

	info, ok := h.cache.Get(deviceID, sensorName)
	if !ok || info.RegionID == nil || *info.RegionID != *newRegion {
		t.Errorf("cache after re-read = %+v (ok=%v), want repopulated with the new region", info, ok)
	}
}

// TestHandleSensorReading_ProcessorOwnApplyConfigRegions_CacheMissRereadsNewRegion
// is FR73's Testing section's second load-bearing case: "same test with the
// assignment written by the processor's own ApplyConfigRegions -- both
// writers are covered." It drives the region change through
// handleConfigAck (this process's own accepted-config-apply path,
// handler.go:322), which is the concrete call site that invokes
// repo.ApplyConfigRegions and, per its own doc comment, publishes one
// invalidation.Event per RegionChange after each has committed. This test
// then applies exactly the event handleConfigAck's loop would have built
// from the RegionChange it returns (Kind: KindRegion, the change's
// DeviceID/SensorName) -- standing in for main.go's Subscriber having
// received it via the broker -- and asserts the very next reading picks up
// the new region, distinct from TestHandleSensorReading_CrossProcessRegionChange_CacheMissRereadsNewRegion
// above only in which writer produced the change.
func TestHandleSensorReading_ProcessorOwnApplyConfigRegions_CacheMissRereadsNewRegion(t *testing.T) {
	const deviceID = "leaflab-ownapply"
	const sensorName = "temp"
	oldRegion := int64ptr(1)
	newRegion := int64ptr(2)

	repo := &stubRepo{
		boardID: 9,
		// stubRepo.ApplyConfigRegions doesn't otherwise report changes (see
		// its default zero-value behaviour above); this is the writer under
		// test -- handleConfigAck's call to it -- reporting that sensor_id 7
		// was reassigned to the new region.
	}
	repo.applyConfigRegionsResult = []RegionChange{{SensorID: 7, SensorName: sensorName, RegionID: *newRegion}}
	repo.getSensorResults = map[string]SensorInfo{
		deviceID + "/" + sensorName: {SensorID: 7, RegionID: newRegion},
	}
	h := newTestHandler(repo)
	h.cache.Set(deviceID, sensorName, SensorInfo{SensorID: 7, RegionID: oldRegion})

	ack := &configpb.DeviceConfigAck{DeviceId: deviceID, AppliedVersion: 3, Accepted: true}
	ackBody, err := proto.Marshal(ack)
	if err != nil {
		t.Fatalf("marshal ack: %v", err)
	}
	// The writer under test: this process's own ApplyConfigRegions, driven
	// through the real handleConfigAck call site (handler.go:322), not a
	// hand-rolled substitute.
	if err := h.handleConfigAck(context.Background(), deviceID, ackBody); err != nil {
		t.Fatalf("handleConfigAck: %v", err)
	}
	if len(repo.applyConfigRegionsCalls) != 1 {
		t.Fatalf("expected 1 ApplyConfigRegions call, got %d", len(repo.applyConfigRegionsCalls))
	}

	// Stand in for main.go's Subscriber delivering the event
	// handleConfigAck's loop published for the RegionChange above (its own
	// invalidationPub is nil in this test -- see newTestHandler's doc
	// comment -- so nothing was actually published; what matters here is
	// that the *change* came from the real ApplyConfigRegions call site).
	ApplyInvalidation(h.cache, invalidation.Event{
		Kind:       invalidation.KindRegion,
		DeviceID:   deviceID,
		SensorID:   7,
		SensorName: sensorName,
	})

	reading := &firmwarepb.SensorReading{Value: 22.0, UptimeMs: 1000}
	body, err := proto.Marshal(reading)
	if err != nil {
		t.Fatalf("marshal reading: %v", err)
	}
	if err := h.handleSensorReading(context.Background(), deviceID, sensorName, body); err != nil {
		t.Fatalf("handleSensorReading: %v", err)
	}

	if len(repo.insertReadingCalls) != 1 {
		t.Fatalf("expected 1 InsertReading call, got %d", len(repo.insertReadingCalls))
	}
	call := repo.insertReadingCalls[0]
	if call.regionID == nil || *call.regionID != *newRegion {
		t.Fatalf("sensor_reading.region_id = %v, want the NEW region %d written by this process's own ApplyConfigRegions", call.regionID, *newRegion)
	}
}

// TestHandleSensorReading_RenameResolvesToSameSensorID is the
// reading-resolution half of the rename load-bearing test --
// TestApplyInvalidation_RenameEvictsPriorNameOnly above covers only the
// cache-map-eviction half. After a rename's invalidation.Event evicts the
// sensor's prior-name cache entry, a reading arriving under the sensor's
// *new* name is a cache miss (the new name was never a cache key before
// this rename -- see SensorCache.Invalidate's doc comment) that must
// resolve, via repo.GetSensor, to the *same* sensor_id the prior name used
// to map to. FR73: "a reading recorded after a rename resolves to the same
// sensor identity and leaves no stale entry under the prior key."
func TestHandleSensorReading_RenameResolvesToSameSensorID(t *testing.T) {
	const deviceID = "leaflab-rename"

	repo := &stubRepo{
		getSensorResults: map[string]SensorInfo{
			deviceID + "/new_light": {SensorID: 5, RegionID: int64ptr(10)},
		},
	}
	h := newTestHandler(repo)
	h.cache.Set(deviceID, "old_light", SensorInfo{SensorID: 5, RegionID: int64ptr(10)})

	// The rename's invalidation.Event (FR52, Phase 5's writer -- this
	// package only needs to prove it handles the event correctly once
	// received, same as the region/identity kinds above).
	ApplyInvalidation(h.cache, invalidation.Event{
		Kind:            invalidation.KindName,
		DeviceID:        deviceID,
		SensorName:      "new_light",
		PriorSensorName: "old_light",
	})

	if _, ok := h.cache.Get(deviceID, "old_light"); ok {
		t.Fatal("prior-name cache entry must be evicted by the rename invalidation before the next reading arrives")
	}

	reading := &firmwarepb.SensorReading{Value: 21.5, UptimeMs: 60000}
	body, err := proto.Marshal(reading)
	if err != nil {
		t.Fatalf("marshal reading: %v", err)
	}
	if err := h.handleSensorReading(context.Background(), deviceID, "new_light", body); err != nil {
		t.Fatalf("handleSensorReading: %v", err)
	}

	if len(repo.insertReadingCalls) != 1 {
		t.Fatalf("expected 1 InsertReading call, got %d", len(repo.insertReadingCalls))
	}
	if got := repo.insertReadingCalls[0].sensorID; got != 5 {
		t.Errorf("InsertReading sensorID = %d, want unchanged 5 (same sensor identity survives the rename)", got)
	}

	if info, ok := h.cache.Get(deviceID, "new_light"); !ok || info.SensorID != 5 {
		t.Errorf("cache after re-read = %+v (ok=%v), want new_light repopulated with SensorID 5", info, ok)
	}
}

// TestHandleSensorReading_RewireAPIIdentityEvent_CacheServesCorrectPostRewireIdentity
// covers FR73 on leaflab/api's explicit RewireSensor RPC (see
// invalidation.KindIdentity's doc comment and server.go's RewireSensor,
// which publishes exactly this event shape after RewireSensorHW commits) --
// the writer surface TestHandleManifest_FR16_3_SimultaneousRewireAndRename_SameSensorID
// above already covers on the *device manifest* path. It reproduces a stale
// cached identity for a device/name key -- as if this process cached it
// before the rewire committed elsewhere -- and asserts the processor's
// *SensorCache itself*, not just what a fresh DB read would say, ends up
// serving the correct post-rewire identity: cache.Get, not only
// repo.GetSensor's return value, must reflect it.
func TestHandleSensorReading_RewireAPIIdentityEvent_CacheServesCorrectPostRewireIdentity(t *testing.T) {
	const deviceID = "leaflab-rewire-cache"
	const sensorName = "temp"

	repo := &stubRepo{
		getSensorResults: map[string]SensorInfo{
			deviceID + "/" + sensorName: {SensorID: 42, RegionID: int64ptr(3)},
		},
	}
	h := newTestHandler(repo)

	// A stale cached identity for this device/name key, predating the
	// rewire.
	h.cache.Set(deviceID, sensorName, SensorInfo{SensorID: 99, RegionID: int64ptr(3)})

	// The invalidation.Event leaflab/api's RewireSensor RPC publishes after
	// RewireSensorHW commits (server.go): SensorID/SensorName as the RPC
	// returns/receives them -- name unchanged, only the hardware key moved.
	ApplyInvalidation(h.cache, invalidation.Event{
		Kind:       invalidation.KindIdentity,
		DeviceID:   deviceID,
		SensorID:   42,
		SensorName: sensorName,
	})

	reading := &firmwarepb.SensorReading{Value: 19.0, UptimeMs: 5000}
	body, err := proto.Marshal(reading)
	if err != nil {
		t.Fatalf("marshal reading: %v", err)
	}
	if err := h.handleSensorReading(context.Background(), deviceID, sensorName, body); err != nil {
		t.Fatalf("handleSensorReading: %v", err)
	}

	if len(repo.insertReadingCalls) != 1 || repo.insertReadingCalls[0].sensorID != 42 {
		t.Fatalf("InsertReading calls = %+v, want exactly 1 with sensorID 42 (correct post-rewire identity)", repo.insertReadingCalls)
	}

	info, ok := h.cache.Get(deviceID, sensorName)
	if !ok || info.SensorID != 42 {
		t.Fatalf("processor SensorCache after rewire = %+v (ok=%v), want SensorID 42 -- the cache, not just the DB, must serve the correct post-rewire identity", info, ok)
	}
}
