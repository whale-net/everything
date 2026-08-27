package main

import (
	"testing"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// This file covers FR39's server-side push validation as wired into
// PushDeviceConfig (config_push.go/server.go), at the Go-dispatch level
// with fakeRepo/fakeAuthz -- the same style as
// server_push_device_config_scope_test.go, reusing its
// newScopeTestServer/sensorAt helpers (an Unclaimed board, so FR1.2/FR1.3's
// household check never fires and these tests isolate FR39's own
// validation gate). config/validate_test.go already covers Validate's
// individual checks in isolation; this file's job is proving the wiring:
// a validation failure refuses the whole push before any write, and every
// failure in a payload reaches the caller together in one multi-detail
// gRPC status (FR39's "a single failure must not mask the rest"), not
// just whichever check found something first.

// TestPushDeviceConfig_CompleteScope_MultipleValidationFailures_AllReturnedInOneStatus
// covers FR39's core wiring contract for scope=COMPLETE: a payload
// failing two independent checks (an out-of-range i2c_address, an
// out-of-range poll_interval_ms) is refused with both failures present in
// the same gRPC status (via contract.AllFailures), each naming its own
// entry and field -- and nothing is written.
func TestPushDeviceConfig_CompleteScope_MultipleValidationFailures_AllReturnedInOneStatus(t *testing.T) {
	repo := &fakeRepo{}
	server, _ := newScopeTestServer(repo)

	_, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Scope:    pb.PushScope_PUSH_SCOPE_COMPLETE,
		Sensors: []*configpb.SensorConfig{
			sensorAt("bad-address", 0x03, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE),                                       // out of range
			{Name: "bad-poll", I2CAddress: 0x40, PollIntervalMs: 1, SensorType: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE}, // below min
		},
	})
	if err == nil {
		t.Fatal("PushDeviceConfig returned nil error for a payload failing two independent FR39 checks, want a refusal")
	}

	failures, ok := contract.AllFailures(err)
	if !ok {
		t.Fatalf("error %v carries no Failure details", err)
	}

	wantFields := map[string]bool{
		"sensors[0].i2c_address":      false,
		"sensors[1].poll_interval_ms": false,
	}
	for _, f := range failures {
		if _, tracked := wantFields[f.Field]; tracked {
			wantFields[f.Field] = true
		}
	}
	for field, found := range wantFields {
		if !found {
			t.Errorf("no failure detail named field %q -- FR39's 'all failures returned together, not just the first' (got fields: %v)", field, failures)
		}
	}
	if len(failures) < 2 {
		t.Errorf("len(AllFailures) = %d, want at least 2", len(failures))
	}

	if len(repo.insertDeviceConfigNextVersionCalls) != 0 {
		t.Errorf("InsertDeviceConfigNextVersion was called %d times, want 0 -- a validation failure must refuse before any write", len(repo.insertDeviceConfigNextVersionCalls))
	}
}

// TestPushDeviceConfig_EditScope_MultipleRemovalFailures_AllReturnedInOneStatus
// covers FR39's wiring for scope=EDIT's two removal-validation cases
// (FR82.4) together: a remove matching nothing in the base and a remove
// naming an unaddressable (unknown-address sentinel) entry both reach the
// caller in the same status, the unaddressable one carrying
// FailureRefusedWithAlternative with its stated remedy -- and nothing is
// written.
func TestPushDeviceConfig_EditScope_MultipleRemovalFailures_AllReturnedInOneStatus(t *testing.T) {
	repo := &fakeRepo{
		getLatestAcceptedConfigResponse: &configpb.DeviceConfig{
			DeviceId: "device-a",
			Sensors:  []*configpb.SensorConfig{sensorAt("kept", 0x10, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE)},
		},
	}
	server, _ := newScopeTestServer(repo)

	_, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Scope:    pb.PushScope_PUSH_SCOPE_EDIT,
		Removes: []*pb.RemoveKey{
			{I2CAddress: 0x99},               // matches nothing in base
			{I2CAddress: 0, SensorType: nil}, // the unknown-address sentinel
		},
	})
	if err == nil {
		t.Fatal("PushDeviceConfig returned nil error for two independent removal failures, want a refusal")
	}

	failures, ok := contract.AllFailures(err)
	if !ok {
		t.Fatalf("error %v carries no Failure details", err)
	}

	byField := map[string]*pb.Failure{}
	for _, f := range failures {
		byField[f.Field] = f
	}

	unknown, ok := byField["removes[0]"]
	if !ok {
		t.Fatalf("no failure detail named field %q (got: %v)", "removes[0]", failures)
	}
	if unknown.Class != string(contract.FailureInvalidArgument) {
		t.Errorf("removes[0] Class = %q, want %q", unknown.Class, contract.FailureInvalidArgument)
	}

	unaddressable, ok := byField["removes[1]"]
	if !ok {
		t.Fatalf("no failure detail named field %q (got: %v)", "removes[1]", failures)
	}
	if unaddressable.Class != string(contract.FailureRefusedWithAlternative) {
		t.Errorf("removes[1] Class = %q, want %q", unaddressable.Class, contract.FailureRefusedWithAlternative)
	}
	if unaddressable.Alternative == "" {
		t.Error("removes[1] carries no Alternative -- FR82.4's stated remedy (push scope=COMPLETE with the entry omitted)")
	}

	if len(repo.insertDeviceConfigNextVersionCalls) != 0 {
		t.Errorf("InsertDeviceConfigNextVersion was called %d times, want 0 -- a validation failure must refuse before any write", len(repo.insertDeviceConfigNextVersionCalls))
	}
}

// TestPushDeviceConfig_CleanEditPayload_NotRefused is the negative-space
// control: an EDIT push that fails none of FR39's checks proceeds past
// validation (reaching the injected storage failure, per
// assertReachedStorage's own doc comment on why this file can't drive a
// push all the way through a real Publish).
func TestPushDeviceConfig_CleanEditPayload_NotRefused(t *testing.T) {
	repo := &fakeRepo{
		getLatestAcceptedConfigResponse: &configpb.DeviceConfig{
			DeviceId: "device-a",
			Sensors:  []*configpb.SensorConfig{sensorAt("kept", 0x10, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE)},
		},
		insertDeviceConfigNextVersionErr: contract.Internal("device_config", "", "injected storage failure"),
	}
	server, _ := newScopeTestServer(repo)

	_, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Scope:    pb.PushScope_PUSH_SCOPE_EDIT,
		Sensors:  []*configpb.SensorConfig{sensorAt("added", 0x20, firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE)},
	})
	assertReachedStorage(t, err)
}
