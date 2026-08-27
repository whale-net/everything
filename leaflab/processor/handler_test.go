package main

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
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

	// Recorded call arguments.
	upsertSensorCalls       []upsertSensorCall
	applyConfigRegionsCalls []applyConfigRegionsCall

	// FR76 (#1342) claim-evidence stubbing/recording -- see
	// checkAndUpdateUptimeWatermarkCalls/satisfyOpenClaimRoundCalls below.
	// checkAndUpdateUptimeWatermarkIsRestart/Err let a test control what
	// CheckAndUpdateUptimeWatermark reports without a real database.
	checkAndUpdateUptimeWatermarkIsRestart bool
	checkAndUpdateUptimeWatermarkErr       error
	checkAndUpdateUptimeWatermarkCalls     []checkAndUpdateUptimeWatermarkCall
	satisfyOpenClaimRoundErr               error
	satisfyOpenClaimRoundCalls             []satisfyOpenClaimRoundCall
}

type checkAndUpdateUptimeWatermarkCall struct {
	boardID          int64
	uptimeS          uint32
	thresholdSeconds uint32
}

type satisfyOpenClaimRoundCall struct {
	deviceID   string
	readingID  int64
	observedAt time.Time
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

func (s *stubRepo) UpsertSensorLabel(_ context.Context, _ int64, _ string) error { return nil }

func (s *stubRepo) UpsertSensorHWHistory(_ context.Context, _ int64, _ *HardwareAddress) error {
	return nil
}

func (s *stubRepo) GetSensor(_ context.Context, _, _ string) (SensorInfo, bool, error) {
	return SensorInfo{}, false, nil
}

func (s *stubRepo) InsertReading(_ context.Context, _ int64, _ *int64, _ float64, _ bool, _ uint32, _ time.Time, _ *int64) (int64, error) {
	return 1, nil
}

func (s *stubRepo) CheckAndUpdateUptimeWatermark(_ context.Context, boardID int64, uptimeS uint32, _ time.Time, thresholdSeconds uint32) (bool, error) {
	s.checkAndUpdateUptimeWatermarkCalls = append(s.checkAndUpdateUptimeWatermarkCalls, checkAndUpdateUptimeWatermarkCall{
		boardID: boardID, uptimeS: uptimeS, thresholdSeconds: thresholdSeconds,
	})
	return s.checkAndUpdateUptimeWatermarkIsRestart, s.checkAndUpdateUptimeWatermarkErr
}

func (s *stubRepo) SatisfyOpenClaimRound(_ context.Context, deviceID string, readingID int64, observedAt time.Time) error {
	s.satisfyOpenClaimRoundCalls = append(s.satisfyOpenClaimRoundCalls, satisfyOpenClaimRoundCall{
		deviceID: deviceID, readingID: readingID, observedAt: observedAt,
	})
	return s.satisfyOpenClaimRoundErr
}

func (s *stubRepo) UpsertDeviceConfig(_ context.Context, _ int64, _ int64, _ []byte) error {
	return nil
}

func (s *stubRepo) AckDeviceConfig(_ context.Context, _ int64, _ int64, _ bool, _ string) error {
	return nil
}

func (s *stubRepo) ApplyConfigRegions(_ context.Context, boardID, version int64) ([]RegionApplySkip, error) {
	s.applyConfigRegionsCalls = append(s.applyConfigRegionsCalls, applyConfigRegionsCall{boardID: boardID, version: version})
	return s.applyConfigRegionsSkips, nil
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
	return NewMessageHandler(slog.Default(), repo, NewSensorCache(), 300)
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
	if call.hw.I2CAddress != 0x23 {
		t.Errorf("I2CAddress: want 0x23, got 0x%x", call.hw.I2CAddress)
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
	if lightCall.hw == nil || lightCall.hw.I2CAddress != 0x23 {
		t.Errorf("light sensor hw address wrong: %+v", lightCall.hw)
	}
	if len(lightCall.hw.MuxPath) != 1 || lightCall.hw.MuxPath[0].MuxAddress != 0x70 || lightCall.hw.MuxPath[0].MuxChannel != 1 {
		t.Errorf("light sensor mux path wrong: %+v", lightCall.hw.MuxPath)
	}

	tempCall := repo.upsertSensorCalls[1]
	if tempCall.hw == nil || tempCall.hw.I2CAddress != 0x44 {
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

// -- FR76 (#1342): claim-evidence wiring in handleSensorReading/handleManifest --

// marshalReading encodes a SensorReading to wire bytes.
func marshalReading(t *testing.T, r *firmwarepb.SensorReading) []byte {
	t.Helper()
	b, err := proto.Marshal(r)
	if err != nil {
		t.Fatalf("marshal reading: %v", err)
	}
	return b
}

// TestHandleSensorReading_RestartSignal_CallsSatisfyOpenClaimRound proves
// the wiring named in handler.go's doc comment: when
// CheckAndUpdateUptimeWatermark reports a restart, handleSensorReading
// calls SatisfyOpenClaimRound for that device_id with the just-written
// reading's id and the observed instant.
func TestHandleSensorReading_RestartSignal_CallsSatisfyOpenClaimRound(t *testing.T) {
	repo := &stubRepo{boardID: 1, sensorID: 10, checkAndUpdateUptimeWatermarkIsRestart: true}
	h := newTestHandler(repo)
	h.cache.Set("leaflab-aabbccdd", "temp", SensorInfo{SensorID: 10, BoardID: 1})

	reading := &firmwarepb.SensorReading{Value: 21.5, UptimeMs: 4000} // uptime_s = 4
	if err := h.handleSensorReading(context.Background(), "leaflab-aabbccdd", "temp", marshalReading(t, reading)); err != nil {
		t.Fatalf("handleSensorReading: %v", err)
	}

	if len(repo.checkAndUpdateUptimeWatermarkCalls) != 1 {
		t.Fatalf("CheckAndUpdateUptimeWatermark calls = %d, want 1", len(repo.checkAndUpdateUptimeWatermarkCalls))
	}
	call := repo.checkAndUpdateUptimeWatermarkCalls[0]
	if call.boardID != 1 {
		t.Errorf("CheckAndUpdateUptimeWatermark boardID = %d, want 1 (the board, not the sensor)", call.boardID)
	}
	if call.uptimeS != 4 {
		t.Errorf("CheckAndUpdateUptimeWatermark uptimeS = %d, want 4 (uptime_ms/1000)", call.uptimeS)
	}

	if len(repo.satisfyOpenClaimRoundCalls) != 1 {
		t.Fatalf("SatisfyOpenClaimRound calls = %d, want 1 -- a restart signal must be forwarded", len(repo.satisfyOpenClaimRoundCalls))
	}
	satisfyCall := repo.satisfyOpenClaimRoundCalls[0]
	if satisfyCall.deviceID != "leaflab-aabbccdd" {
		t.Errorf("SatisfyOpenClaimRound deviceID = %q, want %q", satisfyCall.deviceID, "leaflab-aabbccdd")
	}
}

// TestHandleSensorReading_NoRestartSignal_DoesNotCallSatisfyOpenClaimRound
// is the negative case: an ordinary (non-restart) reading must never touch
// claim-round bookkeeping.
func TestHandleSensorReading_NoRestartSignal_DoesNotCallSatisfyOpenClaimRound(t *testing.T) {
	repo := &stubRepo{boardID: 1, sensorID: 10, checkAndUpdateUptimeWatermarkIsRestart: false}
	h := newTestHandler(repo)
	h.cache.Set("leaflab-aabbccdd", "temp", SensorInfo{SensorID: 10, BoardID: 1})

	reading := &firmwarepb.SensorReading{Value: 21.5, UptimeMs: 600000}
	if err := h.handleSensorReading(context.Background(), "leaflab-aabbccdd", "temp", marshalReading(t, reading)); err != nil {
		t.Fatalf("handleSensorReading: %v", err)
	}

	if len(repo.satisfyOpenClaimRoundCalls) != 0 {
		t.Errorf("SatisfyOpenClaimRound calls = %d, want 0 for a non-restart reading", len(repo.satisfyOpenClaimRoundCalls))
	}
}

// TestHandleSensorReading_WatermarkCheckError_ReadingStillSucceeds proves
// handler.go's stated posture: "a claim-evidence bookkeeping error must
// never nack/requeue (and thus re-attempt writing) an already-successfully-
// written reading" -- a CheckAndUpdateUptimeWatermark error is logged, not
// propagated as handleSensorReading's own error.
func TestHandleSensorReading_WatermarkCheckError_ReadingStillSucceeds(t *testing.T) {
	repo := &stubRepo{boardID: 1, sensorID: 10, checkAndUpdateUptimeWatermarkErr: fmt.Errorf("boom")}
	h := newTestHandler(repo)
	h.cache.Set("leaflab-aabbccdd", "temp", SensorInfo{SensorID: 10, BoardID: 1})

	reading := &firmwarepb.SensorReading{Value: 21.5, UptimeMs: 4000}
	if err := h.handleSensorReading(context.Background(), "leaflab-aabbccdd", "temp", marshalReading(t, reading)); err != nil {
		t.Fatalf("handleSensorReading returned an error from a claim-evidence bookkeeping failure, want nil: %v", err)
	}
	if len(repo.satisfyOpenClaimRoundCalls) != 0 {
		t.Errorf("SatisfyOpenClaimRound calls = %d, want 0 when the watermark check itself errored", len(repo.satisfyOpenClaimRoundCalls))
	}
}

// TestHandleSensorReading_SatisfyOpenClaimRoundError_ReadingStillSucceeds is
// the same posture for the second half of the write: an error from
// SatisfyOpenClaimRound itself (e.g. the migration's uniqueness constraint
// rejecting a double-satisfied reading) must not fail the RPC/message either.
func TestHandleSensorReading_SatisfyOpenClaimRoundError_ReadingStillSucceeds(t *testing.T) {
	repo := &stubRepo{boardID: 1, sensorID: 10, checkAndUpdateUptimeWatermarkIsRestart: true, satisfyOpenClaimRoundErr: fmt.Errorf("boom")}
	h := newTestHandler(repo)
	h.cache.Set("leaflab-aabbccdd", "temp", SensorInfo{SensorID: 10, BoardID: 1})

	reading := &firmwarepb.SensorReading{Value: 21.5, UptimeMs: 4000}
	if err := h.handleSensorReading(context.Background(), "leaflab-aabbccdd", "temp", marshalReading(t, reading)); err != nil {
		t.Fatalf("handleSensorReading returned an error from a claim-evidence bookkeeping failure, want nil: %v", err)
	}
}

// TestHandleManifest_NeverCallsSatisfyOpenClaimRound is the direct
// regression guard for requirement 4's "retained manifest is never
// evidence" -- including the narrow non-retained/zero-readings exception,
// which handleManifest's doc comment flags as deliberately unimplemented
// (never satisfied) because libs/go/rmq's amq.topic consumption path
// carries no MQTT retain flag to distinguish a live publish from a
// broker-replayed retained one (see leaflab/api/ENV.md's "Flagged residual
// gap"). This must hold for every manifest delivery this handler ever
// receives -- there is no code path here that can call
// SatisfyOpenClaimRound at all, so this test protects against that
// changing accidentally (e.g. a future refactor accidentally wiring a
// "since we have a manifest, and this looks like a never-read device"
// shortcut back in without it actually being able to tell retained from
// live).
func TestHandleManifest_NeverCallsSatisfyOpenClaimRound(t *testing.T) {
	repo := &stubRepo{boardID: 1, sensorTypeID: 2, sensorID: 10}
	h := newTestHandler(repo)

	// A manifest for a device that has never had a cache entry (i.e. would
	// be "zero readings ever" in the narrow-exception's terms, the one case
	// requirement 4 permits the manifest itself as evidence for) -- still
	// must never call SatisfyOpenClaimRound, since this handler cannot
	// distinguish retained from live at all.
	manifest := &firmwarepb.DeviceManifest{
		DeviceId: "leaflab-never-read",
		Sensors: []*firmwarepb.SensorDescriptor{
			{Name: "temp", Type: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE, Unit: "°C"},
		},
	}
	if err := h.handleManifest(context.Background(), manifest.DeviceId, marshalManifest(t, manifest)); err != nil {
		t.Fatalf("handleManifest: %v", err)
	}

	if len(repo.satisfyOpenClaimRoundCalls) != 0 {
		t.Errorf("SatisfyOpenClaimRound calls after handleManifest = %d, want 0 -- a manifest delivery (retained or not) must never satisfy a claim round in this handler", len(repo.satisfyOpenClaimRoundCalls))
	}
	if len(repo.checkAndUpdateUptimeWatermarkCalls) != 0 {
		t.Errorf("CheckAndUpdateUptimeWatermark calls after handleManifest = %d, want 0 -- manifest carries no uptime field (requirement 4)", len(repo.checkAndUpdateUptimeWatermarkCalls))
	}
}

// TestHandleManifest_LegacyModeBoardWithReadings_NeverCallsSatisfyOpenClaimRound
// is the issue's "the non-retained-manifest exception applies only to a
// device_id with zero readings ever, not to a device with no accepted
// config" bullet, applied to this handler's actual (unimplemented, per its
// doc comment) posture: a legacy-mode board that has already published
// readings from a compile-time sensor list (no config row, no accepted
// config -- see handleManifest's doc comment on why "no accepted config" is
// deliberately not the exception's trigger) must not qualify for the
// manifest evidence class either. Since the exception is implemented as
// never-satisfied here, this holds trivially for every manifest -- this
// test exists to name the specific legacy-mode scenario the requirement
// text calls out, not because this handler branches on it.
func TestHandleManifest_LegacyModeBoardWithReadings_NeverCallsSatisfyOpenClaimRound(t *testing.T) {
	repo := &stubRepo{boardID: 1, sensorTypeID: 2, sensorID: 10}
	h := newTestHandler(repo)

	deviceID := "leaflab-legacy-mode"
	// Simulate the board already having published readings (legacy-mode
	// firmware, no config row) by pre-populating the cache exactly as
	// handleSensorReading would have left it -- this device_id is NOT
	// "zero readings ever".
	h.cache.Set(deviceID, "temp", SensorInfo{SensorID: 10, BoardID: 1})

	manifest := &firmwarepb.DeviceManifest{
		DeviceId: deviceID,
		Sensors: []*firmwarepb.SensorDescriptor{
			{Name: "temp", Type: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE, Unit: "°C"},
		},
	}
	if err := h.handleManifest(context.Background(), manifest.DeviceId, marshalManifest(t, manifest)); err != nil {
		t.Fatalf("handleManifest: %v", err)
	}

	if len(repo.satisfyOpenClaimRoundCalls) != 0 {
		t.Errorf("SatisfyOpenClaimRound calls after a legacy-mode board's manifest = %d, want 0", len(repo.satisfyOpenClaimRoundCalls))
	}
}
