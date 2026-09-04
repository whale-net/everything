package main

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/configcompose"
	"google.golang.org/protobuf/encoding/protojson"
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

// -- FR9/NFR4 stubs -----------------------------------------------------
// Scaffold-phase stubs: return zero values, since converge is not yet
// wired into handleManifest and no existing test exercises these. The
// Implementation/Testing phases replace stubRepo's use here with a
// dedicated fake that actually tracks inventory/last-accepted/guard state,
// in the style of leaflab/api/server_test.go's fakeRepository.

func (s *stubRepo) ListSensorInventoryForBoard(_ context.Context, _ int64) ([]configcompose.InventorySensor, error) {
	return nil, nil
}

func (s *stubRepo) GetLatestAcceptedConfig(_ context.Context, _ string) (*configpb.DeviceConfig, error) {
	return nil, nil
}

func (s *stubRepo) GetCorrectivePushState(_ context.Context, _ int64) (int32, *int64, error) {
	return 0, nil, nil
}

func (s *stubRepo) InsertCorrectiveConfigNextVersion(_ context.Context, _, _ int64, _ []byte) (int64, error) {
	return 0, nil
}

// fakePublisher records every Publish call -- FR9's corrective push
// publisher, in the same style as leaflab/api/server_test.go's own
// fakePublisher.
type fakePublisher struct {
	published []publishedMessage
}

type publishedMessage struct {
	exchange   string
	routingKey string
	body       interface{}
}

func (p *fakePublisher) Publish(_ context.Context, exchange, routingKey string, body interface{}) error {
	p.published = append(p.published, publishedMessage{exchange: exchange, routingKey: routingKey, body: body})
	return nil
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
	return NewMessageHandler(slog.Default(), repo, NewSensorCache(), &fakePublisher{})
}

// newTestHandlerWithPublisher is newTestHandler, but taking the
// *fakePublisher explicitly so a test can inspect what was published --
// used by the FR9/NFR4 convergence tests below.
func newTestHandlerWithPublisher(repo SensorRepository, pub *fakePublisher) *MessageHandler {
	return NewMessageHandler(slog.Default(), repo, NewSensorCache(), pub)
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

// -- FR9/NFR4: process-internal corrective push convergence ----------------
//
// convergenceRepo extends stubRepo with the state these tests actually need
// to drive: sensor inventory (ComposeDesiredSensors' first input),
// last-accepted config (its second input), and the two Postgres-backed NFR4
// guard columns (corrective_push_attempts / corrective_push_outstanding_version),
// keyed the same way the real Repository (leaflab/processor/repository.go)
// keys them. Unlike stubRepo's own FR9/NFR4 stubs (Scaffold-phase
// placeholders that return zero values), these are wired up for real so a
// test can drive drift detection and both storm guards end to end -- in the
// style of leaflab/api/server_test.go's fakeRepository.
//
// Deliberately not wired: UpsertSensorLabel stays stubRepo's no-op, so the
// fake's inventory Name is never mutated as a side effect of a manifest's
// own writes (unlike the real DB, where sensor_name_history IS what
// ListSensorInventoryForBoard reads). Each test controls
// convergenceRepo.inventory directly instead -- the sequential storm-guard
// test in particular needs every fed manifest to observe the same "prior
// name" baseline, not one that silently self-heals after the first write.
type convergenceRepo struct {
	stubRepo

	inventory    map[int64][]configcompose.InventorySensor // boardID -> inventory
	lastAccepted map[string]*configpb.DeviceConfig          // deviceID -> last accepted config
	pushState    map[int64]correctivePushFixture            // sensorID -> NFR4 guard state
	sensorBoard  map[int64]int64                            // sensorID -> boardID, recorded by InsertCorrectiveConfigNextVersion
	nextVersion  map[int64]int64                            // boardID -> highest device_config.version issued so far

	insertCorrectiveCalls []insertCorrectiveCall
}

// correctivePushFixture mirrors GetCorrectivePushState's two return values.
type correctivePushFixture struct {
	attempts           int32
	outstandingVersion *int64
}

// insertCorrectiveCall is one recorded InsertCorrectiveConfigNextVersion call.
type insertCorrectiveCall struct {
	boardID    int64
	sensorID   int64
	configJSON []byte
	version    int64
}

func newConvergenceRepo(boardID, sensorTypeID, sensorID int64) *convergenceRepo {
	return &convergenceRepo{
		stubRepo:     stubRepo{boardID: boardID, sensorTypeID: sensorTypeID, sensorID: sensorID},
		inventory:    map[int64][]configcompose.InventorySensor{},
		lastAccepted: map[string]*configpb.DeviceConfig{},
		pushState:    map[int64]correctivePushFixture{},
		sensorBoard:  map[int64]int64{},
		nextVersion:  map[int64]int64{},
	}
}

func (r *convergenceRepo) ListSensorInventoryForBoard(_ context.Context, boardID int64) ([]configcompose.InventorySensor, error) {
	return r.inventory[boardID], nil
}

func (r *convergenceRepo) GetLatestAcceptedConfig(_ context.Context, deviceID string) (*configpb.DeviceConfig, error) {
	return r.lastAccepted[deviceID], nil
}

func (r *convergenceRepo) GetCorrectivePushState(_ context.Context, sensorID int64) (int32, *int64, error) {
	s := r.pushState[sensorID]
	return s.attempts, s.outstandingVersion, nil
}

// InsertCorrectiveConfigNextVersion mirrors Repository's own atomic
// behavior: assigns the next per-board version, increments attempts, and
// sets outstandingVersion to that version -- one "transaction" (this fake
// has no concurrent callers within a single test).
func (r *convergenceRepo) InsertCorrectiveConfigNextVersion(_ context.Context, boardID, sensorID int64, configJSON []byte) (int64, error) {
	r.nextVersion[boardID]++
	version := r.nextVersion[boardID]

	s := r.pushState[sensorID]
	s.attempts++
	v := version
	s.outstandingVersion = &v
	r.pushState[sensorID] = s
	r.sensorBoard[sensorID] = boardID

	r.insertCorrectiveCalls = append(r.insertCorrectiveCalls, insertCorrectiveCall{
		boardID: boardID, sensorID: sensorID, configJSON: configJSON, version: version,
	})
	return version, nil
}

// AckDeviceConfig overrides stubRepo's no-op to mirror
// Repository.AckDeviceConfig's own NFR4 behavior: clear
// corrective_push_outstanding_version for whichever sensor on this board had
// exactly this version outstanding, leaving corrective_push_attempts
// untouched -- see repository.go's AckDeviceConfig doc comment for why that
// asymmetry is what makes the storm case reachable.
func (r *convergenceRepo) AckDeviceConfig(_ context.Context, boardID, version int64, _ bool, _ string) error {
	for sensorID, s := range r.pushState {
		if s.outstandingVersion != nil && *s.outstandingVersion == version && r.sensorBoard[sensorID] == boardID {
			s.outstandingVersion = nil
			r.pushState[sensorID] = s
		}
	}
	return nil
}

// resetCorrectivePushState simulates what a fresh FR4 rename (#1770) or an
// explicit FR8 push (#1768) does in production, atomically, in Postgres
// (RenameSensor / PushDeviceConfig -> InsertDeviceConfigNextVersion's
// resetSensorIDs) -- see #1772 Testing criterion 9. Both of those live in
// leaflab/api, not leaflab/processor, so there is nothing in this package to
// call; this directly sets the fake's guard-state fixture to what either
// reset would leave behind.
func (r *convergenceRepo) resetCorrectivePushState(sensorID int64) {
	r.pushState[sensorID] = correctivePushFixture{}
}

// levelRecorder is a minimal slog.Handler that records every record's level
// -- used to assert convergence.go's WARNING/ERROR log-level discipline
// (AGENTS.md § Logging Levels) without depending on a specific slog output
// format.
type levelRecorder struct {
	levels []slog.Level
}

func (r *levelRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (r *levelRecorder) Handle(_ context.Context, rec slog.Record) error {
	r.levels = append(r.levels, rec.Level)
	return nil
}

func (r *levelRecorder) WithAttrs(_ []slog.Attr) slog.Handler { return r }
func (r *levelRecorder) WithGroup(_ string) slog.Handler      { return r }

func (r *levelRecorder) count(level slog.Level) int {
	n := 0
	for _, l := range r.levels {
		if l == level {
			n++
		}
	}
	return n
}

// ackCorrectivePush marshals and delivers an accepted DeviceConfigAck for
// version, mirroring what the device publishes after applying a corrective
// push -- clears the concurrent guard (AckDeviceConfig) without touching
// corrective_push_attempts.
func ackCorrectivePush(t *testing.T, h *MessageHandler, deviceID string, version int64) {
	t.Helper()
	ack := &configpb.DeviceConfigAck{DeviceId: deviceID, AppliedVersion: uint64(version), Accepted: true}
	body, err := proto.Marshal(ack)
	if err != nil {
		t.Fatalf("marshal ack: %v", err)
	}
	if err := h.handleConfigAck(context.Background(), deviceID, body); err != nil {
		t.Fatalf("handleConfigAck: %v", err)
	}
}

// driftingManifest builds a single-sensor manifest reporting name for
// deviceID at the given i2c address -- the shape every convergence test
// below feeds through handleManifest.
func driftingManifest(t *testing.T, deviceID, name string, i2cAddress uint32) []byte {
	t.Helper()
	return marshalManifest(t, &firmwarepb.DeviceManifest{
		DeviceId: deviceID,
		Sensors: []*firmwarepb.SensorDescriptor{
			{Name: name, Type: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE, Unit: "°C", I2CAddress: i2cAddress},
		},
	})
}

// TestHandleManifest_DriftTriggersCorrectivePush is Testing criterion 1: a
// manifest reporting a name different from the DB's current name for that
// sensor issues exactly one corrective push to leaflab.<device>.config.
func TestHandleManifest_DriftTriggersCorrectivePush(t *testing.T) {
	repo := newConvergenceRepo(1, 2, 10)
	repo.inventory[1] = []configcompose.InventorySensor{
		{SensorID: 10, Name: "old-name", I2CAddress: 0x23, Unit: "°C", SensorType: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE},
	}
	pub := &fakePublisher{}
	h := newTestHandlerWithPublisher(repo, pub)

	body := driftingManifest(t, "leaflab-a", "new-name", 0x23)
	if err := h.handleManifest(context.Background(), "leaflab-a", body); err != nil {
		t.Fatalf("handleManifest: %v", err)
	}

	if len(pub.published) != 1 {
		t.Fatalf("expected exactly 1 corrective push, got %d: %+v", len(pub.published), pub.published)
	}
	got := pub.published[0]
	if got.exchange != correctiveConfigExchange {
		t.Errorf("exchange: want %q, got %q", correctiveConfigExchange, got.exchange)
	}
	const wantRoutingKey = "leaflab.leaflab-a.config"
	if got.routingKey != wantRoutingKey {
		t.Errorf("routing key: want %q, got %q", wantRoutingKey, got.routingKey)
	}
}

// TestHandleManifest_NoDriftNoCorrectivePush is Testing criterion 2: a
// manifest whose names all match the DB issues no publish.
func TestHandleManifest_NoDriftNoCorrectivePush(t *testing.T) {
	repo := newConvergenceRepo(1, 2, 10)
	repo.inventory[1] = []configcompose.InventorySensor{
		{SensorID: 10, Name: "same-name", I2CAddress: 0x23, Unit: "°C", SensorType: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE},
	}
	pub := &fakePublisher{}
	h := newTestHandlerWithPublisher(repo, pub)

	body := driftingManifest(t, "leaflab-a", "same-name", 0x23)
	if err := h.handleManifest(context.Background(), "leaflab-a", body); err != nil {
		t.Fatalf("handleManifest: %v", err)
	}

	if len(pub.published) != 0 {
		t.Fatalf("expected no corrective push when the manifest matches the DB, got %d: %+v", len(pub.published), pub.published)
	}
}

// TestHandleManifest_CorrectivePush_CompositionParityAndFullDesiredState is
// Testing criteria 3 and 4: for a fixture DB state, the corrective push's
// composed sensor list is byte-identical to what ComposeDesiredSensors
// itself produces for the equivalent inputs, and it carries every sensor on
// the board -- not only the one that drifted.
func TestHandleManifest_CorrectivePush_CompositionParityAndFullDesiredState(t *testing.T) {
	repo := newConvergenceRepo(1, 2, 10)
	regionID := int64(3)
	repo.inventory[1] = []configcompose.InventorySensor{
		{SensorID: 10, Name: "old-name", I2CAddress: 0x23, Unit: "°C", SensorType: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE},
		{SensorID: 11, Name: "humidity", I2CAddress: 0x44, Unit: "%", SensorType: firmwarepb.SensorType_SENSOR_TYPE_HUMIDITY, RegionID: &regionID},
	}
	lastAcceptedSensors := []*configpb.SensorConfig{
		{I2CAddress: 0x44, Name: "humidity", SensorType: firmwarepb.SensorType_SENSOR_TYPE_HUMIDITY, RegionId: 3, PollIntervalMs: 5000},
	}
	repo.lastAccepted["leaflab-a"] = &configpb.DeviceConfig{DeviceId: "leaflab-a", Sensors: lastAcceptedSensors}

	pub := &fakePublisher{}
	h := newTestHandlerWithPublisher(repo, pub)

	body := driftingManifest(t, "leaflab-a", "new-name", 0x23)
	if err := h.handleManifest(context.Background(), "leaflab-a", body); err != nil {
		t.Fatalf("handleManifest: %v", err)
	}

	if len(repo.insertCorrectiveCalls) != 1 {
		t.Fatalf("expected exactly 1 corrective config recorded, got %d", len(repo.insertCorrectiveCalls))
	}
	var gotCfg configpb.DeviceConfig
	if err := protojson.Unmarshal(repo.insertCorrectiveCalls[0].configJSON, &gotCfg); err != nil {
		t.Fatalf("unmarshal corrective config_json: %v", err)
	}

	// Criterion 4: full desired state -- both board sensors, not just the
	// drifted one (the manifest only reported the i2c=0x23 sensor).
	if len(gotCfg.Sensors) != 2 {
		t.Fatalf("expected corrective push to carry all 2 board sensors, got %d: %+v", len(gotCfg.Sensors), gotCfg.Sensors)
	}

	// Criterion 3: composition parity, asserted against the shared function
	// itself, not a re-implementation.
	wantSensors := configcompose.ComposeDesiredSensors(repo.inventory[1], lastAcceptedSensors, nil)
	wantJSON, err := protojson.Marshal(&configpb.DeviceConfig{Sensors: wantSensors})
	if err != nil {
		t.Fatalf("marshal want sensors: %v", err)
	}
	gotJSON, err := protojson.Marshal(&configpb.DeviceConfig{Sensors: gotCfg.Sensors})
	if err != nil {
		t.Fatalf("marshal got sensors: %v", err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Errorf("corrective push composed sensors not byte-identical to ComposeDesiredSensors' own output:\n got:  %s\n want: %s", gotJSON, wantJSON)
	}
}

// TestHandleManifest_ConcurrentGuardSkipsSecondPush is Testing criterion 5:
// with corrective_push_outstanding_version non-NULL, a second manifest
// reporting the same stale name issues no second push.
func TestHandleManifest_ConcurrentGuardSkipsSecondPush(t *testing.T) {
	repo := newConvergenceRepo(1, 2, 10)
	repo.inventory[1] = []configcompose.InventorySensor{
		{SensorID: 10, Name: "old-name", I2CAddress: 0x23, Unit: "°C", SensorType: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE},
	}
	pub := &fakePublisher{}
	h := newTestHandlerWithPublisher(repo, pub)

	body := driftingManifest(t, "leaflab-a", "stale-name", 0x23)

	if err := h.handleManifest(context.Background(), "leaflab-a", body); err != nil {
		t.Fatalf("first handleManifest: %v", err)
	}
	if len(pub.published) != 1 {
		t.Fatalf("expected 1 push after the first drifting manifest, got %d", len(pub.published))
	}

	// Second manifest, same stale name, no ack in between -- the corrective
	// push from the first manifest is still outstanding, so the concurrent
	// guard must skip issuing a second one.
	if err := h.handleManifest(context.Background(), "leaflab-a", body); err != nil {
		t.Fatalf("second handleManifest: %v", err)
	}
	if len(pub.published) != 1 {
		t.Errorf("expected still only 1 push while a corrective push is outstanding, got %d", len(pub.published))
	}
}

// TestHandleConfigAck_ClearsOutstandingMarkerWithoutTouchingAttempts is
// Testing criterion 6: a matching handleConfigAck sets
// corrective_push_outstanding_version back to NULL without touching
// corrective_push_attempts.
func TestHandleConfigAck_ClearsOutstandingMarkerWithoutTouchingAttempts(t *testing.T) {
	repo := newConvergenceRepo(7, 2, 10)
	outstanding := int64(3)
	repo.pushState[10] = correctivePushFixture{attempts: 1, outstandingVersion: &outstanding}
	repo.sensorBoard[10] = 7
	h := newTestHandler(repo)

	ackCorrectivePush(t, h, "leaflab-a", 3)

	attempts, outstandingVersion, err := repo.GetCorrectivePushState(context.Background(), 10)
	if err != nil {
		t.Fatalf("GetCorrectivePushState: %v", err)
	}
	if outstandingVersion != nil {
		t.Errorf("expected outstanding version cleared, got %d", *outstandingVersion)
	}
	if attempts != 1 {
		t.Errorf("expected corrective_push_attempts left untouched at 1, got %d", attempts)
	}
}

// TestHandleManifest_SequentialStormGuardStopsAfterThreeAttempts is Testing
// criterion 7 (NFR4's own worked example): 3 manifests, each reporting the
// same stale name with a corresponding acked-but-unconverged corrective push
// in between, and a 4th matching manifest issues no 4th corrective push.
func TestHandleManifest_SequentialStormGuardStopsAfterThreeAttempts(t *testing.T) {
	repo := newConvergenceRepo(1, 2, 10)
	repo.inventory[1] = []configcompose.InventorySensor{
		{SensorID: 10, Name: "correct-name", I2CAddress: 0x23, Unit: "°C", SensorType: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE},
	}
	pub := &fakePublisher{}
	h := newTestHandlerWithPublisher(repo, pub)

	body := driftingManifest(t, "leaflab-a", "stale-name", 0x23)

	for i := 0; i < maxCorrectivePushAttempts; i++ {
		if err := h.handleManifest(context.Background(), "leaflab-a", body); err != nil {
			t.Fatalf("manifest %d: %v", i+1, err)
		}
		if len(pub.published) != i+1 {
			t.Fatalf("after manifest %d: expected %d pushes, got %d", i+1, i+1, len(pub.published))
		}
		// Device acks accepted=true (clearing the concurrent guard) but
		// never actually persists the correction to NVS -- its next
		// manifest still reports the stale name.
		ackCorrectivePush(t, h, "leaflab-a", int64(i+1))
	}

	// 4th matching manifest: attempts has reached maxCorrectivePushAttempts,
	// the sequential/reconnect-storm guard stops auto-issuing.
	if err := h.handleManifest(context.Background(), "leaflab-a", body); err != nil {
		t.Fatalf("4th manifest: %v", err)
	}
	if len(pub.published) != maxCorrectivePushAttempts {
		t.Errorf("expected no 4th push once the storm guard gives up, got %d total pushes", len(pub.published))
	}
}

// TestHandleManifest_StormGuardLogLevels is Testing criterion 8: the first
// two failed convergence attempts log at WARNING and the third at ERROR; no
// ERROR is emitted on the first attempt.
func TestHandleManifest_StormGuardLogLevels(t *testing.T) {
	repo := newConvergenceRepo(1, 2, 10)
	repo.inventory[1] = []configcompose.InventorySensor{
		{SensorID: 10, Name: "correct-name", I2CAddress: 0x23, Unit: "°C", SensorType: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE},
	}
	pub := &fakePublisher{}
	rec := &levelRecorder{}
	h := NewMessageHandler(slog.New(rec), repo, NewSensorCache(), pub)

	body := driftingManifest(t, "leaflab-a", "stale-name", 0x23)

	// First attempt: no prior failure yet, so the guard itself must log
	// neither WARNING nor ERROR.
	if err := h.handleManifest(context.Background(), "leaflab-a", body); err != nil {
		t.Fatalf("manifest 1: %v", err)
	}
	if got := rec.count(slog.LevelError); got != 0 {
		t.Errorf("expected 0 ERROR logs after the first attempt, got %d", got)
	}
	ackCorrectivePush(t, h, "leaflab-a", 1)

	// Second and third attempts: each retries a prior unconverged push --
	// WARNING (the system retried to keep going).
	for i := 2; i <= maxCorrectivePushAttempts; i++ {
		if err := h.handleManifest(context.Background(), "leaflab-a", body); err != nil {
			t.Fatalf("manifest %d: %v", i, err)
		}
		ackCorrectivePush(t, h, "leaflab-a", int64(i))
	}
	if got := rec.count(slog.LevelWarn); got != 2 {
		t.Errorf("expected exactly 2 WARNING logs (the first two failed attempts), got %d", got)
	}
	if got := rec.count(slog.LevelError); got != 0 {
		t.Errorf("expected still 0 ERROR logs before the guard gives up, got %d", got)
	}

	// 4th matching manifest: attempts == maxCorrectivePushAttempts, the
	// guard gives up -- ERROR (the operation cannot continue and needs
	// attention, per AGENTS.md § Logging Levels).
	if err := h.handleManifest(context.Background(), "leaflab-a", body); err != nil {
		t.Fatalf("4th manifest: %v", err)
	}
	if got := rec.count(slog.LevelError); got != 1 {
		t.Errorf("expected exactly 1 ERROR log when the storm guard gives up, got %d", got)
	}
	if got := rec.count(slog.LevelWarn); got != 2 {
		t.Errorf("expected WARNING count unchanged at 2 once the guard gives up, got %d", got)
	}
}

// TestHandleManifest_ResetReArmsAfterGuardGivesUp is Testing criterion 9:
// after the guard has given up, a reset (standing in for a fresh FR4 rename
// or an FR8 push -- see resetCorrectivePushState's doc comment) re-arms it,
// and the next drifting manifest issues a push again.
func TestHandleManifest_ResetReArmsAfterGuardGivesUp(t *testing.T) {
	repo := newConvergenceRepo(1, 2, 10)
	repo.inventory[1] = []configcompose.InventorySensor{
		{SensorID: 10, Name: "correct-name", I2CAddress: 0x23, Unit: "°C", SensorType: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE},
	}
	pub := &fakePublisher{}
	h := newTestHandlerWithPublisher(repo, pub)

	body := driftingManifest(t, "leaflab-a", "stale-name", 0x23)

	for i := 0; i < maxCorrectivePushAttempts; i++ {
		if err := h.handleManifest(context.Background(), "leaflab-a", body); err != nil {
			t.Fatalf("manifest %d: %v", i+1, err)
		}
		ackCorrectivePush(t, h, "leaflab-a", int64(i+1))
	}
	if err := h.handleManifest(context.Background(), "leaflab-a", body); err != nil {
		t.Fatalf("4th manifest (guard should give up): %v", err)
	}
	if len(pub.published) != maxCorrectivePushAttempts {
		t.Fatalf("expected the guard to have given up (%d pushes), got %d", maxCorrectivePushAttempts, len(pub.published))
	}

	repo.resetCorrectivePushState(10)

	if err := h.handleManifest(context.Background(), "leaflab-a", body); err != nil {
		t.Fatalf("manifest after reset: %v", err)
	}
	if len(pub.published) != maxCorrectivePushAttempts+1 {
		t.Errorf("expected a new corrective push to be issued after the reset, got %d total pushes", len(pub.published))
	}
}

// TestHandleManifest_StormGuardSurvivesHandlerRestart proves NFR4 § Counter
// persistence: the guard state lives in the repository (production:
// Postgres), never cached on MessageHandler, so a brand-new handler
// instance backed by the same repository must not hand a non-persisting
// device a fresh attempt budget.
//
// Red/green discipline (exercised by hand during Testing, not left as a
// toggle here): temporarily caching attempts on MessageHandler instead of
// reading it fresh from repo on every converge() call turns this test red
// (h2 would start counting from 0 again); restoring the Postgres-backed
// read turns it green.
func TestHandleManifest_StormGuardSurvivesHandlerRestart(t *testing.T) {
	repo := newConvergenceRepo(1, 2, 10)
	repo.inventory[1] = []configcompose.InventorySensor{
		{SensorID: 10, Name: "correct-name", I2CAddress: 0x23, Unit: "°C", SensorType: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE},
	}
	pub := &fakePublisher{}
	h1 := newTestHandlerWithPublisher(repo, pub)

	body := driftingManifest(t, "leaflab-a", "stale-name", 0x23)

	for i := 0; i < maxCorrectivePushAttempts; i++ {
		if err := h1.handleManifest(context.Background(), "leaflab-a", body); err != nil {
			t.Fatalf("manifest %d: %v", i+1, err)
		}
		ackCorrectivePush(t, h1, "leaflab-a", int64(i+1))
	}
	if len(pub.published) != maxCorrectivePushAttempts {
		t.Fatalf("expected %d pushes before restart, got %d", maxCorrectivePushAttempts, len(pub.published))
	}

	// Simulate a leaflab-processor restart: a brand-new MessageHandler (a
	// fresh SensorCache, no in-process state carried over) against the same
	// repository -- production's Postgres, here the same fake backing
	// store.
	h2 := newTestHandlerWithPublisher(repo, pub)
	if err := h2.handleManifest(context.Background(), "leaflab-a", body); err != nil {
		t.Fatalf("manifest after restart: %v", err)
	}
	if len(pub.published) != maxCorrectivePushAttempts {
		t.Errorf("expected the storm guard to survive a handler restart (still %d pushes), got %d", maxCorrectivePushAttempts, len(pub.published))
	}
}
