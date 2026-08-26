package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/rmq"
	"google.golang.org/protobuf/proto"
)

// stubRepo records UpsertSensor and ApplyConfigRegions calls so tests can assert behaviour.
type stubRepo struct {
	// Configurable return values.
	boardID      int64
	sensorTypeID int64
	sensorID     int64

	// Recorded call arguments.
	upsertSensorCalls       []upsertSensorCall
	applyConfigRegionsCalls []applyConfigRegionsCall
	hwHistoryCalls          []hwHistoryCall
	affectedSensors         []AffectedSensor
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

type hwHistoryCall struct {
	sensorID int64
	hw       *HardwareAddress
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

func (s *stubRepo) UpsertSensorHWHistory(_ context.Context, sensorID int64, hw *HardwareAddress) error {
	s.hwHistoryCalls = append(s.hwHistoryCalls, hwHistoryCall{sensorID: sensorID, hw: hw})
	return nil
}

func (s *stubRepo) GetSensor(_ context.Context, _, _ string) (SensorInfo, bool, error) {
	return SensorInfo{}, false, nil
}

func (s *stubRepo) InsertReading(_ context.Context, _ int64, _ *int64, _ float64, _ bool, _ uint32, _ time.Time, _ *int64) error {
	return nil
}

func (s *stubRepo) UpsertDeviceConfig(_ context.Context, _ int64, _ int64, _ []byte) error {
	return nil
}

func (s *stubRepo) AckDeviceConfig(_ context.Context, _ int64, _ int64, _ bool, _ string) error {
	return nil
}

func (s *stubRepo) ApplyConfigRegions(_ context.Context, boardID, version int64) error {
	s.applyConfigRegionsCalls = append(s.applyConfigRegionsCalls, applyConfigRegionsCall{boardID: boardID, version: version})
	return nil
}

func (s *stubRepo) SetSensorChipID(_ context.Context, _ int64, _ string) error { return nil }

func (s *stubRepo) IsKnownChipAddress(_ context.Context, _ string, _ uint32) (bool, error) {
	return true, nil
}
func (s *stubRepo) GetSensorsByBoard(_ context.Context, _ int64) ([]SensorState, error) {
	return nil, nil
}

func (s *stubRepo) GetAffectedSensorsForConfig(_ context.Context, _ int64, _ int64) ([]AffectedSensor, error) {
	return s.affectedSensors, nil
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

// mockCacheInvalidator is a test implementation of CacheInvalidator that records calls.
type mockCacheInvalidator struct {
	mu                     sync.Mutex
	publishedInvalidations []CacheInvalidationSignal
}

func (m *mockCacheInvalidator) PublishInvalidation(ctx context.Context, signal CacheInvalidationSignal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.publishedInvalidations = append(m.publishedInvalidations, signal)
	return nil
}

func (m *mockCacheInvalidator) GetPublished() []CacheInvalidationSignal {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]CacheInvalidationSignal, len(m.publishedInvalidations))
	copy(result, m.publishedInvalidations)
	return result
}

func newTestHandler(repo SensorRepository) *MessageHandler {
	return newTestHandlerWithInvalidator(repo, &mockCacheInvalidator{})
}

func newTestHandlerWithInvalidator(repo SensorRepository, invalidator CacheInvalidator) *MessageHandler {
	return NewMessageHandler(slog.Default(), repo, NewSensorCache(), invalidator)
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

// TestHandleManifest_RewireAndRenameWithRealDB verifies that when a manifest changes both
// a sensor's hardware address AND its name simultaneously, the sensor is still
// identifiable by the hardware address and receives name history updates, not
// a second sensor_id (FR16). This test uses a real database to verify identity
// preservation actually works, not just that the handler calls the repository
// with the right parameters (which is what the stub-based test did).
func TestHandleManifest_RewireAndRenameWithRealDB(t *testing.T) {
	ctx := context.Background()

	// Set up schema with sensor_type, board, sensor, sensor_name_history, sensor_hw_history
	schema := `
		CREATE TABLE sensor_type (
			sensor_type_id BIGSERIAL PRIMARY KEY,
			name VARCHAR(64) NOT NULL UNIQUE,
			default_unit VARCHAR(16) NOT NULL
		);

		INSERT INTO sensor_type (name, default_unit) VALUES
			('temperature', 'degC'),
			('illuminance', 'lx');

		CREATE TABLE board (
			board_id BIGSERIAL PRIMARY KEY,
			device_id VARCHAR(64) NOT NULL UNIQUE,
			registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);

		CREATE TABLE sensor (
			sensor_id BIGSERIAL PRIMARY KEY,
			board_id BIGINT NOT NULL REFERENCES board(board_id) ON DELETE RESTRICT,
			sensor_type_id BIGINT NOT NULL REFERENCES sensor_type(sensor_type_id),
			region_id BIGINT,
			name VARCHAR(128) NOT NULL,
			unit VARCHAR(16) NOT NULL,
			i2c_address SMALLINT,
			mux_path JSONB NOT NULL DEFAULT '[]'::jsonb,
			registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE (board_id, name)
		);

		CREATE UNIQUE INDEX idx_sensor_hw_address
			ON sensor(board_id, i2c_address, sensor_type_id, (mux_path::text))
			WHERE i2c_address IS NOT NULL;

		CREATE TABLE sensor_name_history (
			sensor_name_history_id BIGSERIAL PRIMARY KEY,
			sensor_id BIGINT NOT NULL REFERENCES sensor(sensor_id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			valid_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			valid_to TIMESTAMPTZ
		);

		CREATE TABLE sensor_hw_history (
			history_id BIGSERIAL PRIMARY KEY,
			sensor_id BIGINT NOT NULL REFERENCES sensor(sensor_id) ON DELETE RESTRICT,
			i2c_address SMALLINT,
			mux_path JSONB NOT NULL DEFAULT '[]'::jsonb,
			valid_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			valid_to TIMESTAMPTZ
		);

		CREATE INDEX idx_sensor_hw_history_current
			ON sensor_hw_history(sensor_id) WHERE valid_to IS NULL;
	`

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: schema})

	// Create board and sensor type
	var boardID, sensorTypeID int64
	if err := pg.Pool.QueryRow(ctx, `
		INSERT INTO board (device_id) VALUES ($1) RETURNING board_id
	`, "test-device").Scan(&boardID); err != nil {
		t.Fatalf("insert board: %v", err)
	}

	if err := pg.Pool.QueryRow(ctx, `
		SELECT sensor_type_id FROM sensor_type WHERE name = 'temperature'
	`).Scan(&sensorTypeID); err != nil {
		t.Fatalf("query sensor_type: %v", err)
	}

	// Create an existing sensor with name "old_temp" and address 0x40
	var oldSensorID int64
	muxPath, _ := json.Marshal([]MuxHop{})
	if err := pg.Pool.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, name, unit, i2c_address, mux_path)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb)
		RETURNING sensor_id
	`, boardID, sensorTypeID, "old_temp", "degC", 0x40, muxPath).Scan(&oldSensorID); err != nil {
		t.Fatalf("insert sensor: %v", err)
	}

	// Record initial hardware history
	if _, err := pg.Pool.Exec(ctx, `
		INSERT INTO sensor_name_history (sensor_id, name) VALUES ($1, $2)
	`, oldSensorID, "old_temp"); err != nil {
		t.Fatalf("insert name history: %v", err)
	}

	if _, err := pg.Pool.Exec(ctx, `
		INSERT INTO sensor_hw_history (sensor_id, i2c_address, mux_path) VALUES ($1, $2, $3::jsonb)
	`, oldSensorID, 0x40, muxPath); err != nil {
		t.Fatalf("insert hw history: %v", err)
	}

	// Now send a manifest that changes both address and name simultaneously
	repo := NewRepository(pg.Pool)
	h := newTestHandler(repo)

	manifest := &firmwarepb.DeviceManifest{
		DeviceId: "test-device",
		Sensors: []*firmwarepb.SensorDescriptor{
			{
				Name:       "new_temp", // Different name
				Type:       firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE,
				Unit:       "degC",
				I2CAddress: 0x42, // Different address
			},
		},
	}

	if err := h.handleManifest(ctx, manifest.DeviceId, marshalManifest(t, manifest)); err != nil {
		t.Fatalf("handleManifest: %v", err)
	}

	// Verify that we got the same sensor_id, not a new one
	var actualSensorID int64
	if err := pg.Pool.QueryRow(ctx, `
		SELECT sensor_id FROM sensor WHERE board_id = $1 AND name = $2
	`, boardID, "new_temp").Scan(&actualSensorID); err != nil {
		t.Fatalf("query sensor: %v", err)
	}

	if actualSensorID != oldSensorID {
		t.Errorf("sensor identity not preserved: expected sensor_id=%d, got sensor_id=%d", oldSensorID, actualSensorID)
	}

	// Verify that there's only one sensor (no fork)
	var sensorCount int64
	if err := pg.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM sensor WHERE board_id = $1
	`, boardID).Scan(&sensorCount); err != nil {
		t.Fatalf("count sensors: %v", err)
	}

	if sensorCount != 1 {
		t.Errorf("expected 1 sensor (no fork), got %d", sensorCount)
	}

	// Verify that the sensor has the new address and name
	var name string
	var address int16
	if err := pg.Pool.QueryRow(ctx, `
		SELECT name, i2c_address FROM sensor WHERE sensor_id = $1
	`, actualSensorID).Scan(&name, &address); err != nil {
		t.Fatalf("query sensor details: %v", err)
	}

	if name != "new_temp" {
		t.Errorf("name not updated: expected 'new_temp', got %q", name)
	}

	if address != 0x42 {
		t.Errorf("address not updated: expected 0x42, got 0x%x", address)
	}
}

// TestHandleManifest_HWHistoryRecordsI2CAddress verifies that the hardware address
// is being recorded with full information (i2c_address and mux_path) for the
// canonical key (FR16).
func TestHandleManifest_HWHistoryRecordsI2CAddress(t *testing.T) {
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

	if len(repo.hwHistoryCalls) != 1 {
		t.Fatalf("expected 1 UpsertSensorHWHistory call, got %d", len(repo.hwHistoryCalls))
	}
	call := repo.hwHistoryCalls[0]

	if call.sensorID != 10 {
		t.Errorf("UpsertSensorHWHistory sensorID: want 10, got %d", call.sensorID)
	}
	if call.hw == nil {
		t.Fatal("UpsertSensorHWHistory hw: expected non-nil")
	}
	if call.hw.I2CAddress != 0x23 {
		t.Errorf("UpsertSensorHWHistory i2c_address: want 0x23, got 0x%x", call.hw.I2CAddress)
	}
	if len(call.hw.MuxPath) != 1 {
		t.Fatalf("UpsertSensorHWHistory mux_path: want 1 hop, got %d", len(call.hw.MuxPath))
	}
	if call.hw.MuxPath[0].MuxAddress != 0x70 || call.hw.MuxPath[0].MuxChannel != 1 {
		t.Errorf("UpsertSensorHWHistory mux path: want (0x70, 1), got (0x%x, %d)", call.hw.MuxPath[0].MuxAddress, call.hw.MuxPath[0].MuxChannel)
	}
}

// TestHandleManifest_HWHistoryNilWhenNoAddress verifies that UpsertSensorHWHistory
// is called with nil when a sensor has no hardware address (name fallback path).
func TestHandleManifest_HWHistoryNilWhenNoAddress(t *testing.T) {
	repo := &stubRepo{boardID: 1, sensorTypeID: 2, sensorID: 10}
	h := newTestHandler(repo)

	manifest := &firmwarepb.DeviceManifest{
		DeviceId: "leaflab-aabbccdd",
		Sensors: []*firmwarepb.SensorDescriptor{
			{
				Name: "temp",
				Type: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE,
				Unit: "°C",
				// No I2CAddress
			},
		},
	}

	if err := h.handleManifest(context.Background(), manifest.DeviceId, marshalManifest(t, manifest)); err != nil {
		t.Fatalf("handleManifest: %v", err)
	}

	if len(repo.hwHistoryCalls) != 1 {
		t.Fatalf("expected 1 UpsertSensorHWHistory call, got %d", len(repo.hwHistoryCalls))
	}
	call := repo.hwHistoryCalls[0]

	// When hw is nil, the implementation should still call UpsertSensorHWHistory(ctx, sensorID, nil)
	if call.hw != nil {
		t.Errorf("UpsertSensorHWHistory hw: expected nil, got %+v", call.hw)
	}
}

// TestHandleManifest_MultipleSensorsAllHWHistoryCalled verifies that UpsertSensorHWHistory
// is called for each sensor, whether they have hw addresses or not.
func TestHandleManifest_MultipleSensorsAllHWHistoryCalled(t *testing.T) {
	repo := &stubRepo{boardID: 1, sensorTypeID: 2, sensorID: 10}
	h := newTestHandler(repo)

	manifest := &firmwarepb.DeviceManifest{
		DeviceId: "leaflab-aabbccdd",
		Sensors: []*firmwarepb.SensorDescriptor{
			{Name: "light", Type: firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE, Unit: "lx", I2CAddress: 0x23},
			{Name: "temp", Type: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE, Unit: "°C", I2CAddress: 0x44},
			{Name: "legacy", Type: firmwarepb.SensorType_SENSOR_TYPE_UNKNOWN, Unit: ""},
		},
	}

	if err := h.handleManifest(context.Background(), manifest.DeviceId, marshalManifest(t, manifest)); err != nil {
		t.Fatalf("handleManifest: %v", err)
	}

	if len(repo.hwHistoryCalls) != 3 {
		t.Fatalf("expected 3 UpsertSensorHWHistory calls (one per sensor), got %d", len(repo.hwHistoryCalls))
	}

	// First two should have hw addresses, third should be nil
	if repo.hwHistoryCalls[0].hw == nil || repo.hwHistoryCalls[0].hw.I2CAddress != 0x23 {
		t.Errorf("hwHistoryCalls[0]: expected i2c_address 0x23, got %+v", repo.hwHistoryCalls[0].hw)
	}
	if repo.hwHistoryCalls[1].hw == nil || repo.hwHistoryCalls[1].hw.I2CAddress != 0x44 {
		t.Errorf("hwHistoryCalls[1]: expected i2c_address 0x44, got %+v", repo.hwHistoryCalls[1].hw)
	}
	if repo.hwHistoryCalls[2].hw != nil {
		t.Errorf("hwHistoryCalls[2]: expected nil, got %+v", repo.hwHistoryCalls[2].hw)
	}
}

// TestHandleManifest_SwapDetectionWithRealDB verifies that when a manifest
// exchanges two sensors' hardware keys (a swap), the manifest is rejected
// before any changes are persisted. This test uses a real database to verify
// the swap detection works correctly (FR16 criterion 1b).
// Note: We test swaps with the same sensor type, since the canonical hardware key
// includes type_id, so only same-type sensors can truly swap in the key space.
func TestHandleManifest_SwapDetectionWithRealDB(t *testing.T) {
	ctx := context.Background()

	// Set up schema with sensor_type, board, sensor, sensor_name_history, sensor_hw_history
	schema := `
		CREATE TABLE sensor_type (
			sensor_type_id BIGSERIAL PRIMARY KEY,
			name VARCHAR(64) NOT NULL UNIQUE,
			default_unit VARCHAR(16) NOT NULL
		);

		INSERT INTO sensor_type (name, default_unit) VALUES
			('temperature', 'degC');

		CREATE TABLE board (
			board_id BIGSERIAL PRIMARY KEY,
			device_id VARCHAR(64) NOT NULL UNIQUE,
			registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);

		CREATE TABLE sensor (
			sensor_id BIGSERIAL PRIMARY KEY,
			board_id BIGINT NOT NULL REFERENCES board(board_id) ON DELETE RESTRICT,
			sensor_type_id BIGINT NOT NULL REFERENCES sensor_type(sensor_type_id),
			region_id BIGINT,
			name VARCHAR(128) NOT NULL,
			unit VARCHAR(16) NOT NULL,
			i2c_address SMALLINT,
			mux_path JSONB NOT NULL DEFAULT '[]'::jsonb,
			registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE (board_id, name)
		);

		CREATE UNIQUE INDEX idx_sensor_hw_address
			ON sensor(board_id, i2c_address, sensor_type_id, (mux_path::text))
			WHERE i2c_address IS NOT NULL;

		CREATE TABLE sensor_name_history (
			sensor_name_history_id BIGSERIAL PRIMARY KEY,
			sensor_id BIGINT NOT NULL REFERENCES sensor(sensor_id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			valid_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			valid_to TIMESTAMPTZ
		);

		CREATE TABLE sensor_hw_history (
			history_id BIGSERIAL PRIMARY KEY,
			sensor_id BIGINT NOT NULL REFERENCES sensor(sensor_id) ON DELETE RESTRICT,
			i2c_address SMALLINT,
			mux_path JSONB NOT NULL DEFAULT '[]'::jsonb,
			valid_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			valid_to TIMESTAMPTZ
		);

		CREATE INDEX idx_sensor_hw_history_current
			ON sensor_hw_history(sensor_id) WHERE valid_to IS NULL;
	`

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: schema})

	// Create board and sensor type
	var boardID, sensorTypeID int64
	if err := pg.Pool.QueryRow(ctx, `
		INSERT INTO board (device_id) VALUES ($1) RETURNING board_id
	`, "test-device").Scan(&boardID); err != nil {
		t.Fatalf("insert board: %v", err)
	}

	if err := pg.Pool.QueryRow(ctx, `
		SELECT sensor_type_id FROM sensor_type WHERE name = 'temperature'
	`).Scan(&sensorTypeID); err != nil {
		t.Fatalf("query temperature type: %v", err)
	}

	// Create two existing sensors of the SAME type with different hardware addresses
	var tempSensor1ID, tempSensor2ID int64
	muxPath, _ := json.Marshal([]MuxHop{})

	if err := pg.Pool.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, name, unit, i2c_address, mux_path)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb)
		RETURNING sensor_id
	`, boardID, sensorTypeID, "temperature_1", "degC", 0x40, muxPath).Scan(&tempSensor1ID); err != nil {
		t.Fatalf("insert temperature sensor 1: %v", err)
	}

	if err := pg.Pool.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, name, unit, i2c_address, mux_path)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb)
		RETURNING sensor_id
	`, boardID, sensorTypeID, "temperature_2", "degC", 0x23, muxPath).Scan(&tempSensor2ID); err != nil {
		t.Fatalf("insert temperature sensor 2: %v", err)
	}

	// Record initial hardware history for both
	if _, err := pg.Pool.Exec(ctx, `
		INSERT INTO sensor_name_history (sensor_id, name) VALUES ($1, $2)
	`, tempSensor1ID, "temperature_1"); err != nil {
		t.Fatalf("insert temp1 name history: %v", err)
	}

	if _, err := pg.Pool.Exec(ctx, `
		INSERT INTO sensor_hw_history (sensor_id, i2c_address, mux_path) VALUES ($1, $2, $3::jsonb)
	`, tempSensor1ID, 0x40, muxPath); err != nil {
		t.Fatalf("insert temp1 hw history: %v", err)
	}

	if _, err := pg.Pool.Exec(ctx, `
		INSERT INTO sensor_name_history (sensor_id, name) VALUES ($1, $2)
	`, tempSensor2ID, "temperature_2"); err != nil {
		t.Fatalf("insert temp2 name history: %v", err)
	}

	if _, err := pg.Pool.Exec(ctx, `
		INSERT INTO sensor_hw_history (sensor_id, i2c_address, mux_path) VALUES ($1, $2, $3::jsonb)
	`, tempSensor2ID, 0x23, muxPath); err != nil {
		t.Fatalf("insert temp2 hw history: %v", err)
	}

	// Now send a manifest that SWAPS the hardware addresses (both same type)
	// temperature_1 now has 0x23 (what temperature_2 had)
	// temperature_2 now has 0x40 (what temperature_1 had)
	repo := NewRepository(pg.Pool)
	h := newTestHandler(repo)

	manifest := &firmwarepb.DeviceManifest{
		DeviceId: "test-device",
		Sensors: []*firmwarepb.SensorDescriptor{
			{
				Name:       "temperature_1",
				Type:       firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE,
				Unit:       "degC",
				I2CAddress: 0x23, // Swapped: was 0x40
			},
			{
				Name:       "temperature_2",
				Type:       firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE,
				Unit:       "degC",
				I2CAddress: 0x40, // Swapped: was 0x23
			},
		},
	}

	// Attempt to process the swapped manifest
	err := h.handleManifest(ctx, manifest.DeviceId, marshalManifest(t, manifest))

	// Should get a PermanentError indicating a swap was detected
	if err == nil {
		t.Fatal("expected swap to be rejected, but handleManifest returned nil")
	}

	// The error should be a PermanentError containing information about the swap
	var permErr *rmq.PermanentError
	if !errors.As(err, &permErr) {
		t.Fatalf("expected PermanentError, got %T: %v", err, err)
	}

	// Verify the error message mentions both sensor names
	errMsg := err.Error()
	if !strings.Contains(errMsg, "temperature_1") || !strings.Contains(errMsg, "temperature_2") {
		t.Errorf("error message should mention both sensor names; got: %s", errMsg)
	}

	// Verify no new sensors were created (should still have exactly 2)
	var sensorCount int64
	if err := pg.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM sensor WHERE board_id = $1
	`, boardID).Scan(&sensorCount); err != nil {
		t.Fatalf("count sensors: %v", err)
	}

	if sensorCount != 2 {
		t.Errorf("expected 2 sensors (no fork from swap attempt), got %d", sensorCount)
	}

	// Verify sensor addresses were NOT changed by the failed manifest
	var temp1Addr, temp2Addr int16
	if err := pg.Pool.QueryRow(ctx, `
		SELECT i2c_address FROM sensor WHERE sensor_id = $1
	`, tempSensor1ID).Scan(&temp1Addr); err != nil {
		t.Fatalf("query temp1 sensor address: %v", err)
	}

	if temp1Addr != 0x40 {
		t.Errorf("temperature_1 address should remain 0x40, got 0x%x", temp1Addr)
	}

	if err := pg.Pool.QueryRow(ctx, `
		SELECT i2c_address FROM sensor WHERE sensor_id = $1
	`, tempSensor2ID).Scan(&temp2Addr); err != nil {
		t.Fatalf("query temp2 sensor address: %v", err)
	}

	if temp2Addr != 0x23 {
		t.Errorf("temperature_2 address should remain 0x23, got 0x%x", temp2Addr)
	}
}

func TestHandleManifest_NoSwapWhenOnlyOneHWAddress(t *testing.T) {
	ctx := context.Background()

	schema := `
		CREATE TABLE sensor_type (
			sensor_type_id BIGSERIAL PRIMARY KEY,
			name VARCHAR(64) NOT NULL UNIQUE,
			default_unit VARCHAR(16) NOT NULL
		);

		INSERT INTO sensor_type (name, default_unit) VALUES
			('temperature', 'degC');

		CREATE TABLE board (
			board_id BIGSERIAL PRIMARY KEY,
			device_id VARCHAR(64) NOT NULL UNIQUE,
			registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);

		CREATE TABLE sensor (
			sensor_id BIGSERIAL PRIMARY KEY,
			board_id BIGINT NOT NULL REFERENCES board(board_id) ON DELETE RESTRICT,
			sensor_type_id BIGINT NOT NULL REFERENCES sensor_type(sensor_type_id),
			region_id BIGINT,
			name VARCHAR(128) NOT NULL,
			unit VARCHAR(16) NOT NULL,
			i2c_address SMALLINT,
			mux_path JSONB NOT NULL DEFAULT '[]'::jsonb,
			registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE (board_id, name)
		);

		CREATE UNIQUE INDEX idx_sensor_hw_address
			ON sensor(board_id, i2c_address, sensor_type_id, (mux_path::text))
			WHERE i2c_address IS NOT NULL;

		CREATE TABLE sensor_name_history (
			sensor_name_history_id BIGSERIAL PRIMARY KEY,
			sensor_id BIGINT NOT NULL REFERENCES sensor(sensor_id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			valid_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			valid_to TIMESTAMPTZ
		);

		CREATE TABLE sensor_hw_history (
			history_id BIGSERIAL PRIMARY KEY,
			sensor_id BIGINT NOT NULL REFERENCES sensor(sensor_id) ON DELETE RESTRICT,
			i2c_address SMALLINT,
			mux_path JSONB NOT NULL DEFAULT '[]'::jsonb,
			valid_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			valid_to TIMESTAMPTZ
		);

		CREATE INDEX idx_sensor_hw_history_current
			ON sensor_hw_history(sensor_id) WHERE valid_to IS NULL;
	`

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: schema})

	var boardID, sensorTypeID int64
	if err := pg.Pool.QueryRow(ctx, `
		INSERT INTO board (device_id) VALUES ($1) RETURNING board_id
	`, "test-device").Scan(&boardID); err != nil {
		t.Fatalf("insert board: %v", err)
	}

	if err := pg.Pool.QueryRow(ctx, `
		SELECT sensor_type_id FROM sensor_type WHERE name = 'temperature'
	`).Scan(&sensorTypeID); err != nil {
		t.Fatalf("query sensor_type: %v", err)
	}

	// Create a sensor with a hardware address
	var sensorID int64
	muxPath, _ := json.Marshal([]MuxHop{})
	if err := pg.Pool.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, name, unit, i2c_address, mux_path)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb)
		RETURNING sensor_id
	`, boardID, sensorTypeID, "temp1", "degC", 0x40, muxPath).Scan(&sensorID); err != nil {
		t.Fatalf("insert sensor: %v", err)
	}

	if _, err := pg.Pool.Exec(ctx, `
		INSERT INTO sensor_hw_history (sensor_id, i2c_address, mux_path) VALUES ($1, $2, $3::jsonb)
	`, sensorID, 0x40, muxPath); err != nil {
		t.Fatalf("insert hw history: %v", err)
	}

	// Send a manifest that changes the name and hardware address simultaneously
	// (this is a rewire+rename, not a swap)
	repo := NewRepository(pg.Pool)
	h := newTestHandler(repo)

	manifest := &firmwarepb.DeviceManifest{
		DeviceId: "test-device",
		Sensors: []*firmwarepb.SensorDescriptor{
			{
				Name:       "temp2", // Different name
				Type:       firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE,
				Unit:       "degC",
				I2CAddress: 0x42, // Different address
			},
		},
	}

	// This should NOT be rejected (no swap possible with only one sensor)
	err := h.handleManifest(ctx, manifest.DeviceId, marshalManifest(t, manifest))
	if err != nil {
		t.Fatalf("expected no error for single-sensor rewire+rename, got: %v", err)
	}

	// Verify the sensor was updated in place (same sensor_id)
	var actualSensorID int64
	if err := pg.Pool.QueryRow(ctx, `
		SELECT sensor_id FROM sensor WHERE name = $1
	`, "temp2").Scan(&actualSensorID); err != nil {
		t.Fatalf("query updated sensor: %v", err)
	}

	if actualSensorID != sensorID {
		t.Errorf("sensor should be updated in place, not forked; expected sensor_id=%d, got %d", sensorID, actualSensorID)
	}
}

// Cache invalidation tests (FR73)

// TestCacheInvalidation_RegionChangeDefectRed verifies the defect: before the fix,
// ApplyConfigRegions writes sensor.region_id but the in-memory cache still serves the
// old RegionID. This test demonstrates the defect by breaking the invalidation.
func TestCacheInvalidation_RegionChangeDefectRed(t *testing.T) {
	// SETUP: Create a sensor with region_id = 10
	cache := NewSensorCache()
	cache.Set("device-1", "sensor-1", SensorInfo{SensorID: 100, RegionID: ptrInt64(10)})

	// VERIFY OLD REGION (before invalidation)
	info, ok := cache.Get("device-1", "sensor-1")
	if !ok {
		t.Fatal("sensor should be in cache")
	}
	if info.RegionID == nil || *info.RegionID != 10 {
		t.Fatalf("expected RegionID=10, got %v", info.RegionID)
	}

	// SEND INVALIDATION SIGNAL with new region (20)
	signal := &CacheInvalidationSignal{
		DeviceID:   "device-1",
		SensorID:   100,
		NewName:    "sensor-1",
		RegionID:   ptrInt64(20),
		ChangeType: "region",
	}
	cache.ApplyInvalidation(signal)

	// VERIFY NEW REGION (after invalidation)
	info, ok = cache.Get("device-1", "sensor-1")
	if !ok {
		t.Fatal("sensor should still be in cache")
	}
	if info.RegionID == nil || *info.RegionID != 20 {
		t.Fatalf("expected RegionID=20 after invalidation, got %v", info.RegionID)
	}
}

// TestCacheInvalidation_RenameRemovesOldEntry verifies that a rename invalidation
// removes the entry under the old name and the sensor is findable under the new name.
func TestCacheInvalidation_RenameRemovesOldEntry(t *testing.T) {
	cache := NewSensorCache()

	// SETUP: Create sensor with old name
	cache.Set("device-1", "old_name", SensorInfo{SensorID: 100, RegionID: ptrInt64(5)})

	// VERIFY OLD NAME EXISTS
	info, ok := cache.Get("device-1", "old_name")
	if !ok {
		t.Fatal("sensor should be in cache under old name")
	}
	if info.SensorID != 100 {
		t.Errorf("expected SensorID=100, got %d", info.SensorID)
	}

	// SEND RENAME INVALIDATION SIGNAL
	signal := &CacheInvalidationSignal{
		DeviceID:   "device-1",
		SensorID:   100,
		OldName:    "old_name",
		NewName:    "new_name",
		RegionID:   ptrInt64(5),
		ChangeType: "rename",
	}
	cache.ApplyInvalidation(signal)

	// VERIFY OLD NAME IS GONE
	info, ok = cache.Get("device-1", "old_name")
	if ok {
		t.Error("sensor should NOT be under old name after rename invalidation")
	}

	// VERIFY NEW NAME HAS THE SENSOR
	info, ok = cache.Get("device-1", "new_name")
	if !ok {
		t.Fatal("sensor should be in cache under new name after rename")
	}
	if info.SensorID != 100 {
		t.Errorf("expected SensorID=100 under new name, got %d", info.SensorID)
	}
	if info.RegionID == nil || *info.RegionID != 5 {
		t.Errorf("expected RegionID=5 preserved through rename, got %v", info.RegionID)
	}
}

// TestCacheInvalidation_ReWireInvalidatesAll verifies that a rewire invalidation
// clears all sensors for a device to force a reload.
func TestCacheInvalidation_ReWireInvalidatesAll(t *testing.T) {
	cache := NewSensorCache()

	// SETUP: Create multiple sensors on a device
	cache.Set("device-1", "sensor-1", SensorInfo{SensorID: 100, RegionID: ptrInt64(1)})
	cache.Set("device-1", "sensor-2", SensorInfo{SensorID: 101, RegionID: ptrInt64(2)})
	cache.Set("device-1", "sensor-3", SensorInfo{SensorID: 102, RegionID: ptrInt64(3)})

	// VERIFY SENSORS EXIST
	for i := 1; i <= 3; i++ {
		name := fmt.Sprintf("sensor-%d", i)
		info, ok := cache.Get("device-1", name)
		if !ok {
			t.Fatalf("sensor %s should be in cache", name)
		}
		if info.SensorID != int64(99+i) {
			t.Errorf("expected SensorID=%d, got %d", 99+i, info.SensorID)
		}
	}

	// SEND REWIRE INVALIDATION (clears all sensors for device)
	signal := &CacheInvalidationSignal{
		DeviceID:   "device-1",
		SensorID:   100,
		ChangeType: "rewire",
	}
	cache.ApplyInvalidation(signal)

	// VERIFY ALL SENSORS ARE GONE
	for i := 1; i <= 3; i++ {
		name := fmt.Sprintf("sensor-%d", i)
		_, ok := cache.Get("device-1", name)
		if ok {
			t.Errorf("sensor %s should NOT be in cache after rewire", name)
		}
	}
}

// TestCacheInvalidation_FanOut verifies that when multiple subscribers listen,
// all of them receive the invalidation signal (broadcast, not queue).
func TestCacheInvalidation_FanOut(t *testing.T) {
	// SETUP: Two independent caches that both subscribe to invalidations
	cache1 := NewSensorCache()
	cache2 := NewSensorCache()

	cache1.Set("device-1", "sensor-1", SensorInfo{SensorID: 100, RegionID: ptrInt64(10)})
	cache2.Set("device-1", "sensor-1", SensorInfo{SensorID: 100, RegionID: ptrInt64(10)})

	// SEND SAME INVALIDATION TO BOTH (simulating broadcast)
	signal := CacheInvalidationSignal{
		DeviceID:   "device-1",
		SensorID:   100,
		NewName:    "sensor-1",
		RegionID:   ptrInt64(20),
		ChangeType: "region",
	}

	cache1.ApplyInvalidation(&signal)
	cache2.ApplyInvalidation(&signal)

	// VERIFY BOTH ARE UPDATED
	info1, _ := cache1.Get("device-1", "sensor-1")
	info2, _ := cache2.Get("device-1", "sensor-1")

	if info1.RegionID == nil || *info1.RegionID != 20 {
		t.Errorf("cache1: expected RegionID=20, got %v", info1.RegionID)
	}
	if info2.RegionID == nil || *info2.RegionID != 20 {
		t.Errorf("cache2: expected RegionID=20, got %v", info2.RegionID)
	}
}

// TestCacheInvalidation_PublishedOnConfigAck verifies that when a config ack
// triggers ApplyConfigRegions, cache invalidation signals are published for
// all affected sensors.
func TestCacheInvalidation_PublishedOnConfigAck(t *testing.T) {
	invalidator := &mockCacheInvalidator{}
	repo := &stubRepo{
		boardID: 1,
		affectedSensors: []AffectedSensor{
			{DeviceID: "device-1", SensorID: 100, Name: "sensor-1", RegionID: ptrInt64(5)},
			{DeviceID: "device-1", SensorID: 101, Name: "sensor-2", RegionID: ptrInt64(6)},
		},
	}

	h := newTestHandlerWithInvalidator(repo, invalidator)

	ack := &configpb.DeviceConfigAck{
		DeviceId:       "device-1",
		AppliedVersion: 1,
		Accepted:       true,
	}
	body, err := proto.Marshal(ack)
	if err != nil {
		t.Fatalf("marshal ack: %v", err)
	}

	if err := h.handleConfigAck(context.Background(), "device-1", body); err != nil {
		t.Fatalf("handleConfigAck: %v", err)
	}

	// VERIFY SIGNALS WERE PUBLISHED
	published := invalidator.GetPublished()
	if len(published) != 2 {
		t.Fatalf("expected 2 invalidation signals, got %d", len(published))
	}

	// VERIFY EACH SIGNAL
	for i, signal := range published {
		if signal.DeviceID != "device-1" {
			t.Errorf("signal[%d].DeviceID: want device-1, got %s", i, signal.DeviceID)
		}
		if signal.ChangeType != "region" {
			t.Errorf("signal[%d].ChangeType: want region, got %s", i, signal.ChangeType)
		}
		if i == 0 {
			if signal.SensorID != 100 || signal.RegionID == nil || *signal.RegionID != 5 {
				t.Errorf("signal[0]: want SensorID=100, RegionID=5, got %d, %v", signal.SensorID, signal.RegionID)
			}
		} else {
			if signal.SensorID != 101 || signal.RegionID == nil || *signal.RegionID != 6 {
				t.Errorf("signal[1]: want SensorID=101, RegionID=6, got %d, %v", signal.SensorID, signal.RegionID)
			}
		}
	}
}

// Helper functions

func ptrInt64(v int64) *int64 {
	return &v
}
