package main

import (
	"strings"
	"testing"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/authz"
	pushconfig "github.com/whale-net/everything/leaflab/api/config"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// This file covers FR82's config push scope semantics at the Go-dispatch
// level (fakeRepo/fakeAuthz, no Docker/network) -- the same style as
// server_push_device_config_test.go's FR1.2/FR1.3 coverage. A refused push
// returns before touching s.publisher (a concrete *rmq.Publisher this repo
// has no in-process fake for), and every "reaches storage" test here
// inspects repo.insertDeviceConfigNextVersionCalls rather than driving the
// RPC all the way through a real publish -- see that file's own doc
// comment for why.
//
// scopeTestBoardRef/newScopeTestServer name the board Unclaimed, which
// skips FR1.2/FR1.3's household check entirely (no region_id is ever set
// on this file's sensors) so these tests isolate FR82's own scope
// semantics from that unrelated check.

const scopeTestBoardID = int64(55)

var scopeTestBoardRef = authz.EntityRef{Kind: authz.EntityBoard, ID: scopeTestBoardID}

func newScopeTestServer(repo *fakeRepo) (*LeafLabAPIServer, *fakeAuthz) {
	authzSvc := &fakeAuthz{
		resolveResponses: map[authz.EntityRef]authz.Resolution{
			scopeTestBoardRef: {Unclaimed: true},
		},
	}
	repo.getOrCreateBoardID = scopeTestBoardID
	return newPushValidationServer(repo, authzSvc), authzSvc
}

func sensorAt(name string, i2c uint32, st firmwarepb.SensorType) *configpb.SensorConfig {
	return &configpb.SensorConfig{Name: name, I2CAddress: i2c, SensorType: st}
}

// assertReachedStorage asserts err is the injected storage failure (an
// fakeRepo.insertDeviceConfigNextVersionErr every "success path" test in
// this file configures) rather than nil: s.publisher is a concrete
// *rmq.Publisher this repo has no in-process fake for, so a genuinely
// successful push can't be driven all the way through Publish here (see
// this file's own doc comment, and
// push_device_config_invariant_integration_test.go's identical note).
// Injecting a storage error stops execution one step short of Publish
// while still proving every FR82 branch above it -- scope validation,
// materialisation, entries assembly -- ran and reached
// InsertDeviceConfigNextVersion.
func assertReachedStorage(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("PushDeviceConfig returned nil error, want the injected storage failure (test setup) -- s.publisher is nil and would panic if actually reached")
	}
	detail, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error %v carries no Failure detail", err)
	}
	if detail.Class != string(contract.FailureInternal) {
		t.Fatalf("Class = %q, want %q (the injected storage failure, proving this ran past every FR82 validation/materialisation step)", detail.Class, contract.FailureInternal)
	}
}

// -- FR82.1: scope is required, with no default, and checked before any
// board bookkeeping or write --------------------------------------------

// TestPushDeviceConfig_UnspecifiedScope_RefusedBeforeAnyWrite is FR82.1's
// core assertion: an omitted scope is rejected with a distinct
// machine-readable failure class naming the `scope` field, and *nothing*
// runs before it -- not even GetOrCreateBoard's self-registration upsert,
// proving no board bookkeeping, no config write, and (by construction,
// since Publish is only ever reached after InsertDeviceConfigNextVersion)
// no publish happens either.
func TestPushDeviceConfig_UnspecifiedScope_RefusedBeforeAnyWrite(t *testing.T) {
	repo := &fakeRepo{}
	server, _ := newScopeTestServer(repo)

	_, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Scope:    pb.PushScope_PUSH_SCOPE_UNSPECIFIED,
		Sensors:  []*configpb.SensorConfig{sensorAt("light", 0x23, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE)},
	})
	if err == nil {
		t.Fatal("PushDeviceConfig with PUSH_SCOPE_UNSPECIFIED returned nil error, want a refusal")
	}
	detail, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error %v carries no Failure detail", err)
	}
	if detail.Class != string(contract.FailureInvalidArgument) {
		t.Errorf("Class = %q, want %q", detail.Class, contract.FailureInvalidArgument)
	}
	if detail.Field != "scope" {
		t.Errorf("Field = %q, want %q (naming the omitted field)", detail.Field, "scope")
	}

	if repo.getOrCreateBoardCalls != 0 {
		t.Errorf("GetOrCreateBoard was called %d times, want 0 -- an unspecified scope must refuse before any board bookkeeping", repo.getOrCreateBoardCalls)
	}
	if len(repo.insertDeviceConfigNextVersionCalls) != 0 {
		t.Errorf("InsertDeviceConfigNextVersion was called %d times, want 0 -- nothing may be stored", len(repo.insertDeviceConfigNextVersionCalls))
	}
}

// TestPushDeviceConfig_UnspecifiedScope_NeverInferredFromPayloadShape
// covers FR82.1's "scope is never inferred from payload shape, entry
// count, request size, caller identity, or which endpoint was called":
// a payload that looks exactly like a legitimate COMPLETE push (a full
// sensor set) and a single-entry payload are both refused identically
// when scope is left unspecified -- the failure class/field never changes
// based on how the payload happens to look.
func TestPushDeviceConfig_UnspecifiedScope_NeverInferredFromPayloadShape(t *testing.T) {
	cases := []struct {
		name    string
		sensors []*configpb.SensorConfig
	}{
		{"single-entry", []*configpb.SensorConfig{sensorAt("light", 0x23, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE)}},
		{"full-set-shaped", []*configpb.SensorConfig{
			sensorAt("light", 0x23, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE),
			sensorAt("temperature", 0x44, firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE),
			sensorAt("humidity", 0x44, firmwarepb.SensorType_SENSOR_TYPE_HUMIDITY),
		}},
		{"empty", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepo{}
			server, _ := newScopeTestServer(repo)

			_, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
				DeviceId: "device-a",
				Scope:    pb.PushScope_PUSH_SCOPE_UNSPECIFIED,
				Sensors:  tc.sensors,
			})
			detail, ok := contract.FromError(err)
			if !ok {
				t.Fatalf("error %v carries no Failure detail", err)
			}
			if detail.Class != string(contract.FailureInvalidArgument) || detail.Field != "scope" {
				t.Errorf("Class/Field = %q/%q, want %q/%q regardless of payload shape", detail.Class, detail.Field, contract.FailureInvalidArgument, "scope")
			}
			if len(repo.insertDeviceConfigNextVersionCalls) != 0 {
				t.Errorf("payload shape %q caused a write despite unspecified scope", tc.name)
			}
		})
	}
}

// TestPushDeviceConfig_IdenticalPayload_ScopeAloneChangesBehavior is the
// companion "not inferred" proof from the other direction: the exact same
// Sensors payload, pushed with different explicit scope values, is
// handled differently (COMPLETE stores as-submitted; EDIT materialises
// against a base) -- behavior tracks the explicit field, never the
// payload's own shape.
func TestPushDeviceConfig_IdenticalPayload_ScopeAloneChangesBehavior(t *testing.T) {
	sensors := []*configpb.SensorConfig{sensorAt("light", 0x23, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE)}

	completeRepo := &fakeRepo{
		resolveSensorTypeIDResponses:     map[string]int64{"illuminance": 1},
		insertDeviceConfigNextVersionErr: errInjectedStorageFailure,
	}
	completeServer, _ := newScopeTestServer(completeRepo)
	_, err := completeServer.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Scope:    pb.PushScope_PUSH_SCOPE_COMPLETE,
		Sensors:  sensors,
	})
	assertReachedStorage(t, err)
	if completeRepo.getLatestAcceptedConfigCalls != 0 {
		t.Errorf("COMPLETE push consulted GetLatestAcceptedConfig %d times, want 0 -- COMPLETE has no base to materialise against", completeRepo.getLatestAcceptedConfigCalls)
	}

	editRepo := &fakeRepo{
		resolveSensorTypeIDResponses: map[string]int64{"illuminance": 1},
	}
	editServer, _ := newScopeTestServer(editRepo)
	if _, err := editServer.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Scope:    pb.PushScope_PUSH_SCOPE_EDIT,
		Sensors:  sensors,
	}); err == nil {
		t.Fatal("EDIT push with no accepted base returned nil error, want FR82.3's refusal")
	}
	if editRepo.getLatestAcceptedConfigCalls != 1 {
		t.Errorf("EDIT push consulted GetLatestAcceptedConfig %d times, want 1 -- EDIT must load a base", editRepo.getLatestAcceptedConfigCalls)
	}
}

// -- FR82.2: COMPLETE ------------------------------------------------------

// TestPushDeviceConfig_Complete_AllEntriesAuthored_NoBaseConsulted proves
// a COMPLETE push stores every entry as authored and never even looks up
// an accepted base -- FR82.2's "the payload is the board's entire desired
// sensor set".
func TestPushDeviceConfig_Complete_AllEntriesAuthored_NoBaseConsulted(t *testing.T) {
	repo := &fakeRepo{
		resolveSensorTypeIDResponses:     map[string]int64{"illuminance": 1, "temperature": 2},
		insertDeviceConfigNextVersionErr: errInjectedStorageFailure,
	}
	server, _ := newScopeTestServer(repo)

	_, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Scope:    pb.PushScope_PUSH_SCOPE_COMPLETE,
		Sensors: []*configpb.SensorConfig{
			sensorAt("light", 0x23, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE),
			sensorAt("temp", 0x44, firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE),
		},
	})
	assertReachedStorage(t, err)
	if len(repo.insertDeviceConfigNextVersionCalls) != 1 {
		t.Fatalf("InsertDeviceConfigNextVersion called %d times, want 1", len(repo.insertDeviceConfigNextVersionCalls))
	}
	entries := repo.insertDeviceConfigNextVersionCalls[0].entries
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	for _, e := range entries {
		if e.Provenance != pushconfig.ProvenanceAuthored {
			t.Errorf("entry %q Provenance = %q, want %q", e.Sensor.Name, e.Provenance, pushconfig.ProvenanceAuthored)
		}
	}
}

// TestPushDeviceConfig_Complete_WithRemoves_Refused proves scope=COMPLETE
// rejects a non-empty removes list -- COMPLETE removes an entry by
// omitting it from sensors, not via `removes`.
func TestPushDeviceConfig_Complete_WithRemoves_Refused(t *testing.T) {
	repo := &fakeRepo{}
	server, _ := newScopeTestServer(repo)

	_, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Scope:    pb.PushScope_PUSH_SCOPE_COMPLETE,
		Sensors:  []*configpb.SensorConfig{sensorAt("light", 0x23, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE)},
		Removes:  []*pb.RemoveKey{{I2CAddress: 0x44}},
	})
	if err == nil {
		t.Fatal("COMPLETE push with a non-empty removes list returned nil error, want a refusal")
	}
	detail, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error %v carries no Failure detail", err)
	}
	if detail.Class != string(contract.FailureInvalidArgument) {
		t.Errorf("Class = %q, want %q", detail.Class, contract.FailureInvalidArgument)
	}
	if detail.Field != "removes" {
		t.Errorf("Field = %q, want %q", detail.Field, "removes")
	}
	if len(repo.insertDeviceConfigNextVersionCalls) != 0 {
		t.Errorf("InsertDeviceConfigNextVersion was called %d times, want 0", len(repo.insertDeviceConfigNextVersionCalls))
	}
}

// TestPushDeviceConfig_Complete_FirstPush_Works proves COMPLETE succeeds
// against a board with no prior config at all (FR82.2: "always works --
// including as a board's first push") -- fakeRepo's GetLatestAcceptedConfig
// isn't even called on the COMPLETE branch, so there is no base to be
// missing in the first place.
func TestPushDeviceConfig_Complete_FirstPush_Works(t *testing.T) {
	repo := &fakeRepo{
		resolveSensorTypeIDResponses:     map[string]int64{"illuminance": 1},
		insertDeviceConfigNextVersionErr: errInjectedStorageFailure,
	}
	server, _ := newScopeTestServer(repo)

	_, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Scope:    pb.PushScope_PUSH_SCOPE_COMPLETE,
		Sensors:  []*configpb.SensorConfig{sensorAt("light", 0x23, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE)},
	})
	// COMPLETE reaching InsertDeviceConfigNextVersion at all -- for a board
	// with no prior config -- is FR82.2's "always works, including as a
	// board's first push" (fakeRepo.getOrCreateBoardID always succeeds,
	// mirroring GetOrCreateBoard's real self-registration upsert).
	assertReachedStorage(t, err)
	if len(repo.insertDeviceConfigNextVersionCalls) != 1 {
		t.Fatalf("InsertDeviceConfigNextVersion called %d times, want 1", len(repo.insertDeviceConfigNextVersionCalls))
	}
}

// -- FR82.3: EDIT ------------------------------------------------------

// TestPushDeviceConfig_Edit_NoAcceptedConfig_RefusedWithExactSentence
// covers FR82.3's exact stated refusal condition, verbatim -- never a
// generic validation failure.
func TestPushDeviceConfig_Edit_NoAcceptedConfig_RefusedWithExactSentence(t *testing.T) {
	repo := &fakeRepo{} // GetLatestAcceptedConfig returns nil, nil by default
	server, _ := newScopeTestServer(repo)

	_, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Scope:    pb.PushScope_PUSH_SCOPE_EDIT,
		Sensors:  []*configpb.SensorConfig{sensorAt("light", 0x23, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE)},
	})
	if err == nil {
		t.Fatal("EDIT push with no accepted config returned nil error, want a refusal")
	}
	detail, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error %v carries no Failure detail", err)
	}
	if detail.Class != string(contract.FailureRefusedWithAlternative) {
		t.Errorf("Class = %q, want %q (FR59.3 refuse-and-name-the-alternative)", detail.Class, contract.FailureRefusedWithAlternative)
	}
	const wantReason = "This board has no accepted config to complete your edit from; send a complete push."
	if detail.Reason != wantReason {
		t.Errorf("Reason = %q, want the exact stated sentence %q", detail.Reason, wantReason)
	}
	if len(repo.insertDeviceConfigNextVersionCalls) != 0 {
		t.Errorf("InsertDeviceConfigNextVersion was called %d times, want 0", len(repo.insertDeviceConfigNextVersionCalls))
	}
}

// TestPushDeviceConfig_Edit_MaterialisesUnnamedEntriesFromBase covers
// FR82.3/FR82.4's core EDIT contract at the full-handler level: entries
// named in the push are authored; entries carried forward from the base
// (never named by the caller) are materialised.
func TestPushDeviceConfig_Edit_MaterialisesUnnamedEntriesFromBase(t *testing.T) {
	repo := &fakeRepo{
		resolveSensorTypeIDResponses:     map[string]int64{"illuminance": 1, "temperature": 2},
		insertDeviceConfigNextVersionErr: errInjectedStorageFailure,
	}
	repo.getOrCreateBoardID = scopeTestBoardID
	baseCfg := &configpb.DeviceConfig{
		Sensors: []*configpb.SensorConfig{
			sensorAt("light", 0x23, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE), // untouched by this push
		},
	}
	repo.getLatestAcceptedConfigResponse = baseCfg

	server, _ := newScopeTestServer(repo)

	_, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Scope:    pb.PushScope_PUSH_SCOPE_EDIT,
		Sensors:  []*configpb.SensorConfig{sensorAt("temp", 0x44, firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE)},
	})
	assertReachedStorage(t, err)
	if len(repo.insertDeviceConfigNextVersionCalls) != 1 {
		t.Fatalf("InsertDeviceConfigNextVersion called %d times, want 1", len(repo.insertDeviceConfigNextVersionCalls))
	}
	entries := repo.insertDeviceConfigNextVersionCalls[0].entries
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2 (1 materialised + 1 authored)", len(entries))
	}
	byName := map[string]pushconfig.Provenance{}
	for _, e := range entries {
		byName[e.Sensor.Name] = e.Provenance
	}
	if byName["light"] != pushconfig.ProvenanceMaterialised {
		t.Errorf("'light' Provenance = %q, want %q", byName["light"], pushconfig.ProvenanceMaterialised)
	}
	if byName["temp"] != pushconfig.ProvenanceAuthored {
		t.Errorf("'temp' Provenance = %q, want %q", byName["temp"], pushconfig.ProvenanceAuthored)
	}
}

// TestPushDeviceConfig_Edit_MaterialisationBase_NeverTheStaleManifest
// proves the EDIT materialisation base comes from GetLatestAcceptedConfig
// alone: fakeRepo has no notion of a "reported manifest" at all
// (LoadBoardSensorIdentities -- the only other board-state fakeRepo
// exposes -- returns nil, and is only consulted by FR16/FR17's identity
// check, never by resolveConfigEntries/Materialise), so a base entry
// resolveConfigEntries can't see at all proves by construction that
// nothing but GetLatestAcceptedConfig's return value ever contributes to
// EDIT's base -- see FR82.3's "the materialisation base is the board's
// current accepted config version alone".
func TestPushDeviceConfig_Edit_MaterialisationBase_NeverTheStaleManifest(t *testing.T) {
	repo := &fakeRepo{
		resolveSensorTypeIDResponses:     map[string]int64{"illuminance": 1},
		insertDeviceConfigNextVersionErr: errInjectedStorageFailure,
	}
	repo.getOrCreateBoardID = scopeTestBoardID
	repo.getLatestAcceptedConfigResponse = &configpb.DeviceConfig{
		Sensors: []*configpb.SensorConfig{
			sensorAt("accepted-light", 0x23, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE),
		},
	}
	// loadBoardSensorIdentitiesResponse simulates a device manifest that
	// disagrees with the accepted config (a different name at the same
	// hardware key) -- if EDIT's base ever leaked from here instead of
	// GetLatestAcceptedConfig, the materialised entry would be named
	// "manifest-light".
	repo.loadBoardSensorIdentitiesResponse = []BoardSensorIdentity{
		{SensorID: 1, Name: "manifest-light", SensorTypeID: 1},
	}

	server, _ := newScopeTestServer(repo)

	_, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Scope:    pb.PushScope_PUSH_SCOPE_EDIT,
		Sensors:  nil, // no adds/changes -- everything must carry forward from the accepted base
	})
	assertReachedStorage(t, err)
	entries := repo.insertDeviceConfigNextVersionCalls[0].entries
	if len(entries) != 1 || entries[0].Sensor.Name != "accepted-light" {
		t.Fatalf("materialised entries = %+v, want only 'accepted-light' (the manifest must contribute nothing)", entries)
	}
}

// -- FR82.4: removal keys ------------------------------------------------

// TestPushDeviceConfig_Edit_ChipKeyRemove_DropsBothEntriesAtThatChip
// covers FR82.4's chip-key removal form at the full-handler level: a
// CCS811 chip key drops both its eCO2 and TVOC entries in one push.
func TestPushDeviceConfig_Edit_ChipKeyRemove_DropsBothEntriesAtThatChip(t *testing.T) {
	repo := &fakeRepo{
		resolveSensorTypeIDResponses:     map[string]int64{"eco2": 1, "tvoc": 2},
		insertDeviceConfigNextVersionErr: errInjectedStorageFailure,
	}
	repo.getOrCreateBoardID = scopeTestBoardID
	repo.getLatestAcceptedConfigResponse = &configpb.DeviceConfig{
		Sensors: []*configpb.SensorConfig{
			sensorAt("eco2", 0x5A, firmwarepb.SensorType_SENSOR_TYPE_ECO2),
			sensorAt("tvoc", 0x5A, firmwarepb.SensorType_SENSOR_TYPE_TVOC),
		},
	}
	server, _ := newScopeTestServer(repo)

	_, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Scope:    pb.PushScope_PUSH_SCOPE_EDIT,
		Removes:  []*pb.RemoveKey{{I2CAddress: 0x5A}}, // chip key: sensor_type absent
	})
	assertReachedStorage(t, err)
	call := repo.insertDeviceConfigNextVersionCalls[0]
	if len(call.entries) != 0 {
		t.Fatalf("entries = %+v, want empty (both eco2 and tvoc dropped)", call.entries)
	}
	// FR82.4: both dropped entries carry RemoveFormChipKey -- the
	// materialisation-side half of "which form was used is stated back to
	// the caller" (server.go's removeFormToProto/RemovedEntry wiring onto
	// the wire response is covered separately -- see
	// TestRemoveFormToProto -- since a genuinely successful push can't be
	// driven through here; see assertReachedStorage's own doc comment).
	if len(call.removed) != 2 {
		t.Fatalf("removed = %+v, want 2 (both eco2 and tvoc)", call.removed)
	}
	for _, re := range call.removed {
		if re.Form != pushconfig.RemoveFormChipKey {
			t.Errorf("removed entry %q Form = %v, want RemoveFormChipKey", re.Entry.Sensor.Name, re.Form)
		}
	}
}

// TestPushDeviceConfig_Edit_FullKeyRemove_DropsExactlyOneEntry proves a
// full canonical key (sensor_type present) drops only the one entry it
// names, leaving a sibling entry at the same chip untouched.
func TestPushDeviceConfig_Edit_FullKeyRemove_DropsExactlyOneEntry(t *testing.T) {
	repo := &fakeRepo{
		resolveSensorTypeIDResponses:     map[string]int64{"temperature": 1, "humidity": 2},
		insertDeviceConfigNextVersionErr: errInjectedStorageFailure,
	}
	repo.getOrCreateBoardID = scopeTestBoardID
	repo.getLatestAcceptedConfigResponse = &configpb.DeviceConfig{
		Sensors: []*configpb.SensorConfig{
			sensorAt("temperature", 0x44, firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE),
			sensorAt("humidity", 0x44, firmwarepb.SensorType_SENSOR_TYPE_HUMIDITY),
		},
	}
	server, _ := newScopeTestServer(repo)

	humidity := firmwarepb.SensorType_SENSOR_TYPE_HUMIDITY
	_, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Scope:    pb.PushScope_PUSH_SCOPE_EDIT,
		Removes:  []*pb.RemoveKey{{I2CAddress: 0x44, SensorType: &humidity}},
	})
	assertReachedStorage(t, err)
	call := repo.insertDeviceConfigNextVersionCalls[0]
	entries := call.entries
	if len(entries) != 1 || entries[0].Sensor.Name != "temperature" {
		t.Fatalf("entries = %+v, want only 'temperature' to survive", entries)
	}
	// FR82.4: the dropped entry ("humidity") carries RemoveFormFullKey --
	// see TestPushDeviceConfig_Edit_ChipKeyRemove_DropsBothEntriesAtThatChip's
	// own comment for why the wire response itself isn't asserted here.
	if len(call.removed) != 1 || call.removed[0].Entry.Sensor.Name != "humidity" {
		t.Fatalf("removed = %+v, want exactly 'humidity'", call.removed)
	}
	if call.removed[0].Form != pushconfig.RemoveFormFullKey {
		t.Errorf("removed[0].Form = %v, want RemoveFormFullKey", call.removed[0].Form)
	}
}

// TestPushDeviceConfig_Edit_RemoveUnknownAddressSentinel_RefusedNotSilentNoOp
// covers FR82.4/FR39: a remove naming the unknown-address sentinel
// (i2c_address == 0, proto3's zero value for a non-optional uint32 -- the
// wire has no way to distinguish "explicitly 0" from "omitted") is
// rejected with a distinct reason and the scope=COMPLETE remedy, never a
// silent no-op.
func TestPushDeviceConfig_Edit_RemoveUnknownAddressSentinel_RefusedNotSilentNoOp(t *testing.T) {
	repo := &fakeRepo{}
	repo.getOrCreateBoardID = scopeTestBoardID
	repo.getLatestAcceptedConfigResponse = &configpb.DeviceConfig{
		Sensors: []*configpb.SensorConfig{{Name: "mystery"}}, // i2c_address left at 0 (unknown)
	}
	server, _ := newScopeTestServer(repo)

	_, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Scope:    pb.PushScope_PUSH_SCOPE_EDIT,
		Removes:  []*pb.RemoveKey{{I2CAddress: 0}},
	})
	if err == nil {
		t.Fatal("EDIT remove naming i2c_address=0 returned nil error, want a refusal")
	}
	detail, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error %v carries no Failure detail", err)
	}
	if detail.Class != string(contract.FailureRefusedWithAlternative) {
		t.Errorf("Class = %q, want %q", detail.Class, contract.FailureRefusedWithAlternative)
	}
	if !strings.Contains(detail.Alternative, "scope=COMPLETE") {
		t.Errorf("Alternative = %q, want it to name the scope=COMPLETE remedy", detail.Alternative)
	}
	if len(repo.insertDeviceConfigNextVersionCalls) != 0 {
		t.Errorf("InsertDeviceConfigNextVersion was called %d times, want 0 -- an unaddressable remove must write nothing", len(repo.insertDeviceConfigNextVersionCalls))
	}
}

// TestPushDeviceConfig_Edit_FiveAddsPlusTwoRemoves_OneVersion covers
// FR82.5: multiple edits and removals in one push still assign exactly
// one config version -- not one per sensor or one per removal.
func TestPushDeviceConfig_Edit_FiveAddsPlusTwoRemoves_OneVersion(t *testing.T) {
	repo := &fakeRepo{
		resolveSensorTypeIDResponses:     map[string]int64{"illuminance": 1, "eco2": 2, "tvoc": 3},
		insertDeviceConfigNextVersionErr: errInjectedStorageFailure,
	}
	repo.getOrCreateBoardID = scopeTestBoardID
	repo.getLatestAcceptedConfigResponse = &configpb.DeviceConfig{
		Sensors: []*configpb.SensorConfig{
			sensorAt("keep", 0x01, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE),
			sensorAt("drop-full", 0x02, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE),
			sensorAt("drop-chip-a", 0x03, firmwarepb.SensorType_SENSOR_TYPE_ECO2),
			sensorAt("drop-chip-b", 0x03, firmwarepb.SensorType_SENSOR_TYPE_TVOC),
		},
	}
	server, _ := newScopeTestServer(repo)

	illuminance := firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE
	_, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Scope:    pb.PushScope_PUSH_SCOPE_EDIT,
		Sensors: []*configpb.SensorConfig{
			sensorAt("add-1", 0x10, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE),
			sensorAt("add-2", 0x11, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE),
			sensorAt("add-3", 0x12, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE),
			sensorAt("add-4", 0x13, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE),
			sensorAt("add-5", 0x14, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE),
		},
		Removes: []*pb.RemoveKey{
			{I2CAddress: 0x02, SensorType: &illuminance},
			{I2CAddress: 0x03}, // chip key
		},
	})
	assertReachedStorage(t, err)
	if len(repo.insertDeviceConfigNextVersionCalls) != 1 {
		t.Fatalf("InsertDeviceConfigNextVersion called %d times, want exactly 1 for the whole batch", len(repo.insertDeviceConfigNextVersionCalls))
	}
	entries := repo.insertDeviceConfigNextVersionCalls[0].entries
	if len(entries) != 6 { // keep + 5 adds
		t.Fatalf("len(entries) = %d, want 6", len(entries))
	}
}

// -- Unresolved sensor_type: stored, provenance skipped, never rejected --

// TestPushDeviceConfig_UnresolvedSensorType_StillStoredNotRejected covers
// this task's documented behavior for a single-virtual chip (e.g. BH1750,
// which reports no explicit sensor_type): resolveConfigEntries must not
// fail or drop the entry solely because its sensor_type can't be
// resolved against the catalog -- it's still present in the entries this
// handler hands to storage, carrying the hwkey "unresolved" sentinel
// (SensorTypeID 0), which InsertDeviceConfigNextVersion (repository.go)
// is documented to skip a provenance row for, without rejecting the push.
func TestPushDeviceConfig_UnresolvedSensorType_StillStoredNotRejected(t *testing.T) {
	// resolveSensorTypeIDResponses empty: every type name "not found".
	repo := &fakeRepo{insertDeviceConfigNextVersionErr: errInjectedStorageFailure}
	server, _ := newScopeTestServer(repo)

	_, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Scope:    pb.PushScope_PUSH_SCOPE_COMPLETE,
		Sensors:  []*configpb.SensorConfig{{Name: "light", I2CAddress: 0x23, ChipType: configpb.ChipType_CHIP_TYPE_BH1750}},
	})
	// Reaching InsertDeviceConfigNextVersion at all (rather than a
	// validation refusal) is the assertion this test cares about: an
	// unresolvable sensor_type must never cause PushDeviceConfig to
	// refuse the entry or the push.
	assertReachedStorage(t, err)
	entries := repo.insertDeviceConfigNextVersionCalls[0].entries
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1 -- an unresolvable sensor_type must still be stored", len(entries))
	}
	if entries[0].Key.SensorTypeID != 0 {
		t.Errorf("SensorTypeID = %d, want the unresolved sentinel 0", entries[0].Key.SensorTypeID)
	}
	if entries[0].Sensor.Name != "light" {
		t.Errorf("Sensor.Name = %q, want %q -- the entry itself must be unaffected", entries[0].Sensor.Name, "light")
	}
}

// -- FR82.4: "which removal form was used" onto the wire response ----------

// TestRemoveFormToProto proves removeFormToProto (server.go) -- the one
// place that translates config.RemoveForm onto the wire pb.RemoveForm
// PushDeviceConfigResponse.RemovedEntry.form carries -- maps both real
// forms correctly and never produces REMOVE_FORM_UNSPECIFIED for either.
// The rest of the response-population path (building
// []*pb.RemovedEntry from a Materialise Result.Removed) can't be exercised
// end to end here -- see assertReachedStorage's doc comment on why a
// genuinely successful push can't be driven through this file's fakeRepo;
// TestPushDeviceConfig_Edit_FullKeyRemove_DropsExactlyOneEntry and
// TestPushDeviceConfig_Edit_ChipKeyRemove_DropsBothEntriesAtThatChip above
// already prove config.Materialise's Form value survives unchanged up to
// InsertDeviceConfigNextVersion's removed argument, immediately upstream
// of where this function is called.
func TestRemoveFormToProto(t *testing.T) {
	cases := []struct {
		name string
		in   pushconfig.RemoveForm
		want pb.RemoveForm
	}{
		{"full key", pushconfig.RemoveFormFullKey, pb.RemoveForm_REMOVE_FORM_FULL_KEY},
		{"chip key", pushconfig.RemoveFormChipKey, pb.RemoveForm_REMOVE_FORM_CHIP_KEY},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := removeFormToProto(tc.in); got != tc.want {
				t.Errorf("removeFormToProto(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
