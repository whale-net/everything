package main

import (
	"testing"
	"time"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// This file covers FR38 (dry run) and FR48 (multi-board push, push_group
// status) at the Go-dispatch level (fakeRepo/fakeAuthz), the same style as
// server_push_device_config_scope_test.go and
// server_push_device_config_test.go.
//
// FR38's noopConfigWriter never touches s.publisher (server.go's own doc
// comment on PushDeviceConfig's writer selection) -- unlike every
// pre-existing test in this package, a dry_run push can be driven all the
// way to a genuine BoardPushResult{Success: true} here without the
// "inject a storage error to stop one step short of Publish" workaround
// (assertReachedStorage in server_push_device_config_scope_test.go): there
// is no writer.write step here that ever reaches a *rmq.Publisher at all.
// A non-dry-run (real) multi-board push still can't be driven past
// Publish in this file, so those tests keep using the injected-storage-
// error pattern, exactly as the rest of this package does.

const (
	multiBoardIDA = int64(101)
	multiBoardIDB = int64(102)
)

// -- FR38: dry run --------------------------------------------------------

// TestPushDeviceConfig_DryRun_Edit_ReturnsEffectiveConfigNotPartialPayload
// proves a dry-run EDIT push's effective_config is the board's complete
// materialised set (base carried forward plus the authored add), not the
// partial add-only payload the request submitted.
func TestPushDeviceConfig_DryRun_Edit_ReturnsEffectiveConfigNotPartialPayload(t *testing.T) {
	repo := &fakeRepo{
		resolveSensorTypeIDResponses: map[string]int64{"temperature": 1, "humidity": 2},
		getLatestAcceptedConfigResponse: &configpb.DeviceConfig{
			Sensors: []*configpb.SensorConfig{
				sensorAt("temperature", 0x44, firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE),
			},
		},
	}
	server, _ := newScopeTestServer(repo)

	resp, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Scope:    pb.PushScope_PUSH_SCOPE_EDIT,
		Sensors:  []*configpb.SensorConfig{sensorAt("humidity", 0x44, firmwarepb.SensorType_SENSOR_TYPE_HUMIDITY)},
		DryRun:   true,
	})
	if err != nil {
		t.Fatalf("dry-run PushDeviceConfig returned an error, want success: %v", err)
	}
	if len(resp.Results) != 1 || !resp.Results[0].Success {
		t.Fatalf("Results = %+v, want one successful result", resp.Results)
	}
	got := resp.Results[0].EffectiveConfig.GetSensors()
	if len(got) != 2 {
		t.Fatalf("effective_config has %d sensors, want 2 (the carried-forward base plus the authored add) -- got only the submitted partial payload: %+v", len(got), got)
	}
	names := map[string]bool{}
	for _, s := range got {
		names[s.Name] = true
	}
	if !names["temperature"] || !names["humidity"] {
		t.Errorf("effective_config sensors = %+v, want both 'temperature' (carried forward) and 'humidity' (authored)", got)
	}

	if len(repo.insertDeviceConfigNextVersionCalls) != 0 {
		t.Errorf("InsertDeviceConfigNextVersion was called %d times, want 0 -- a dry run must write nothing", len(repo.insertDeviceConfigNextVersionCalls))
	}
}

// TestPushDeviceConfig_DryRun_ReturnsDiffAndWouldAssignVersion proves a
// dry-run push returns the diff against the board's prior config and the
// version PeekNextConfigVersion says a real push would assign next --
// never the zero value, and never actually reserved.
func TestPushDeviceConfig_DryRun_ReturnsDiffAndWouldAssignVersion(t *testing.T) {
	repo := &fakeRepo{peekNextConfigVersionResponse: 7}
	server, _ := newScopeTestServer(repo)

	resp, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Scope:    pb.PushScope_PUSH_SCOPE_COMPLETE,
		Sensors:  []*configpb.SensorConfig{sensorAt("light", 0x23, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE)},
		DryRun:   true,
	})
	if err != nil {
		t.Fatalf("dry-run PushDeviceConfig returned an error, want success: %v", err)
	}
	result := resp.Results[0]
	if !result.Success {
		t.Fatalf("Results[0].Success = false, want true: %+v", result.Failure)
	}
	if result.Version != 7 {
		t.Errorf("Version = %d, want 7 (PeekNextConfigVersion's configured return -- the version a real push would assign)", result.Version)
	}
	if len(result.Diff) != 1 || result.Diff[0].Kind != pb.DiffKind_DIFF_KIND_ADDED {
		t.Errorf("Diff = %+v, want one DIFF_KIND_ADDED entry (no prior accepted config)", result.Diff)
	}
}

// TestPushDeviceConfig_DryRun_WritesNoRowPublishesNothing is FR38's own
// explicit assertion: a dry run leaves zero device_config rows, creates no
// push_group, and never reaches the publisher. The publish half is proven
// structurally, not just by omission: s.publisher is nil here (see
// newScopeTestServer/newPushValidationServer), so PushDeviceConfig would
// panic on a nil-pointer dereference if the dry-run path ever reached
// liveConfigWriter.write's Publish call -- this test succeeding without a
// panic is itself the assertion that no publish was attempted.
func TestPushDeviceConfig_DryRun_WritesNoRowPublishesNothing(t *testing.T) {
	repo := &fakeRepo{
		getLatestAcceptedConfigResponse: &configpb.DeviceConfig{
			Sensors: []*configpb.SensorConfig{
				sensorAt("light", 0x23, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE),
			},
		},
	}
	server, _ := newScopeTestServer(repo)

	resp, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Scope:    pb.PushScope_PUSH_SCOPE_EDIT,
		Removes:  []*pb.RemoveKey{{I2CAddress: 0x23}},
		DryRun:   true,
	})
	if err != nil {
		t.Fatalf("dry-run PushDeviceConfig returned an error, want success (no panic reaching a nil publisher): %v", err)
	}
	if !resp.Results[0].Success {
		t.Fatalf("Results[0].Success = false, want true: %+v", resp.Results[0].Failure)
	}
	if len(repo.insertDeviceConfigNextVersionCalls) != 0 {
		t.Errorf("InsertDeviceConfigNextVersion was called %d times, want 0", len(repo.insertDeviceConfigNextVersionCalls))
	}
	if len(repo.createPushGroupCalls) != 0 {
		t.Errorf("CreatePushGroup was called %d times, want 0 -- a dry run must never create a push_group row", len(repo.createPushGroupCalls))
	}
	if resp.PushGroupId != 0 {
		t.Errorf("PushGroupId = %d, want 0 (unset) for a dry run", resp.PushGroupId)
	}
}

// TestPushDeviceConfig_DryRun_Complete_AccidentalOmission_ShowsRemovalBeforeLanding
// is FR38's core scenario: a COMPLETE push that forgot to include an entry
// the board already has accepted shows that entry in the removals set,
// full-key form, before anything lands.
func TestPushDeviceConfig_DryRun_Complete_AccidentalOmission_ShowsRemovalBeforeLanding(t *testing.T) {
	repo := &fakeRepo{
		getLatestAcceptedConfigResponse: &configpb.DeviceConfig{
			Sensors: []*configpb.SensorConfig{
				sensorAt("light", 0x23, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE),
				sensorAt("temperature", 0x44, firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE),
			},
		},
	}
	server, _ := newScopeTestServer(repo)

	// "light" is accidentally left off this COMPLETE payload.
	resp, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Scope:    pb.PushScope_PUSH_SCOPE_COMPLETE,
		Sensors:  []*configpb.SensorConfig{sensorAt("temperature", 0x44, firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE)},
		DryRun:   true,
	})
	if err != nil {
		t.Fatalf("dry-run PushDeviceConfig returned an error, want success: %v", err)
	}
	result := resp.Results[0]
	if !result.Success {
		t.Fatalf("Success = false, want true: %+v", result.Failure)
	}
	if len(result.Removed) != 1 {
		t.Fatalf("Removed = %+v, want exactly one entry (the accidentally-omitted 'light')", result.Removed)
	}
	if result.Removed[0].I2CAddress != 0x23 {
		t.Errorf("Removed[0].I2CAddress = %#x, want 0x23 (light)", result.Removed[0].I2CAddress)
	}
	if result.Removed[0].Form != pb.RemoveForm_REMOVE_FORM_FULL_KEY {
		t.Errorf("Removed[0].Form = %v, want REMOVE_FORM_FULL_KEY -- a COMPLETE-scope omission is a distinct entry, never a chip-key group", result.Removed[0].Form)
	}
}

// TestPushDeviceConfig_DryRun_ChipKeyRemove_ExpandsToTwoEntries proves
// FR82.4/FR38's "a chip key is expanded into the individual entries it
// drops": an EDIT push's chip-key remove for a CCS811-shaped board (eCO2 +
// TVOC at the same i2c_address) lists both entries in the dry run's
// removal set, not one grouped entry.
func TestPushDeviceConfig_DryRun_ChipKeyRemove_ExpandsToTwoEntries(t *testing.T) {
	repo := &fakeRepo{
		resolveSensorTypeIDResponses: map[string]int64{"eco2": 1, "tvoc": 2},
		getLatestAcceptedConfigResponse: &configpb.DeviceConfig{
			Sensors: []*configpb.SensorConfig{
				sensorAt("eco2", 0x5A, firmwarepb.SensorType_SENSOR_TYPE_ECO2),
				sensorAt("tvoc", 0x5A, firmwarepb.SensorType_SENSOR_TYPE_TVOC),
			},
		},
	}
	server, _ := newScopeTestServer(repo)

	resp, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Scope:    pb.PushScope_PUSH_SCOPE_EDIT,
		Removes:  []*pb.RemoveKey{{I2CAddress: 0x5A}}, // chip key: sensor_type absent
		DryRun:   true,
	})
	if err != nil {
		t.Fatalf("dry-run PushDeviceConfig returned an error, want success: %v", err)
	}
	result := resp.Results[0]
	if !result.Success {
		t.Fatalf("Success = false, want true: %+v", result.Failure)
	}
	if len(result.Removed) != 2 {
		t.Fatalf("Removed = %+v, want 2 entries (a CCS811 chip key drops both eCO2 and TVOC)", result.Removed)
	}
	for _, re := range result.Removed {
		if re.Form != pb.RemoveForm_REMOVE_FORM_CHIP_KEY {
			t.Errorf("removed entry i2c=%#x Form = %v, want REMOVE_FORM_CHIP_KEY", re.I2CAddress, re.Form)
		}
		if re.I2CAddress != 0x5A {
			t.Errorf("removed entry I2CAddress = %#x, want 0x5A", re.I2CAddress)
		}
	}
}

// -- FR48: multi-board push -----------------------------------------------

// TestPushDeviceConfig_MultiBoard_DryRun_BothSucceed_PerBoardResults is
// FR38+FR48's composition: a dry run over a multi-board set returns one
// result per board, never a single aggregate.
func TestPushDeviceConfig_MultiBoard_DryRun_BothSucceed_PerBoardResults(t *testing.T) {
	boardARef := authz.EntityRef{Kind: authz.EntityBoard, ID: multiBoardIDA}
	boardBRef := authz.EntityRef{Kind: authz.EntityBoard, ID: multiBoardIDB}

	repo := &fakeRepo{
		getOrCreateBoardIDs: map[string]int64{"device-a": multiBoardIDA, "device-b": multiBoardIDB},
	}
	authzSvc := &fakeAuthz{
		resolveResponses: map[authz.EntityRef]authz.Resolution{
			boardARef: {Unclaimed: true},
			boardBRef: {Unclaimed: true},
		},
	}
	server := newPushValidationServer(repo, authzSvc)

	resp, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceIds: []string{"device-a", "device-b"},
		Scope:     pb.PushScope_PUSH_SCOPE_COMPLETE,
		Sensors:   []*configpb.SensorConfig{sensorAt("light", 0x23, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE)},
		DryRun:    true,
		Reason:    "coordinated firmware bump",
	})
	if err != nil {
		t.Fatalf("dry-run multi-board PushDeviceConfig returned an error, want success: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("Results = %+v, want exactly 2 (one per board), never collapsed to one aggregate", resp.Results)
	}
	if resp.Results[0].DeviceId != "device-a" || !resp.Results[0].Success {
		t.Errorf("Results[0] = %+v, want a successful result for device-a", resp.Results[0])
	}
	if resp.Results[1].DeviceId != "device-b" || !resp.Results[1].Success {
		t.Errorf("Results[1] = %+v, want a successful result for device-b", resp.Results[1])
	}
}

// TestPushDeviceConfig_MultiBoard_PerBoardResults_MixSuccessAndFailure is
// FR48.1's core assertion: one board's own refusal never blocks or is
// masked by another board's success in the same call -- never one
// aggregate success or one aggregate failure.
//
// device-a and device-b resolve to different (claimed) households, so the
// caller's Scope must reach both (a union of household 1 and 2) for the
// board-set spanning check to pass; the payload names a region belonging
// to household 2, which validatePushRegions accepts for device-a (whose
// own household is 2) and refuses for device-b (whose own household is
// 1) -- isolating one board's per-board failure from the other's success.
func TestPushDeviceConfig_MultiBoard_PerBoardResults_MixSuccessAndFailure(t *testing.T) {
	const regionID = 55
	boardARef := authz.EntityRef{Kind: authz.EntityBoard, ID: multiBoardIDA}
	boardBRef := authz.EntityRef{Kind: authz.EntityBoard, ID: multiBoardIDB}
	regionRef := authz.EntityRef{Kind: authz.EntityRegion, ID: regionID}

	repo := &fakeRepo{
		getOrCreateBoardIDs: map[string]int64{"device-a": multiBoardIDA, "device-b": multiBoardIDB},
	}
	authzSvc := &fakeAuthz{
		scope: authz.NewUnionScope(authz.NewHouseholdScope(1), authz.NewHouseholdScope(2)),
		resolveResponses: map[authz.EntityRef]authz.Resolution{
			boardARef: {HouseholdID: 2},
			boardBRef: {HouseholdID: 1},
			regionRef: {HouseholdID: 2},
		},
	}
	server := newPushValidationServer(repo, authzSvc)

	resp, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceIds: []string{"device-a", "device-b"},
		Scope:     pb.PushScope_PUSH_SCOPE_COMPLETE,
		Sensors:   []*configpb.SensorConfig{{Name: "region-sensor", RegionId: regionID}},
		DryRun:    true,
		Reason:    "coordinated firmware bump",
	})
	if err != nil {
		t.Fatalf("multi-board PushDeviceConfig returned an RPC-level error, want per-board results: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("Results = %+v, want exactly 2", resp.Results)
	}

	a, b := resp.Results[0], resp.Results[1]
	if a.DeviceId != "device-a" || !a.Success {
		t.Errorf("Results[0] (device-a) = %+v, want a successful result (its own household matches the region's)", a)
	}
	if b.DeviceId != "device-b" || b.Success {
		t.Errorf("Results[1] (device-b) = %+v, want a per-board failure (its household differs from the region's)", b)
	}
	if b.Failure == nil || b.Failure.Class != string(contract.FailureInvalidArgument) {
		t.Errorf("Results[1].Failure = %+v, want a FailureInvalidArgument naming the foreign region", b.Failure)
	}

	if len(repo.insertDeviceConfigNextVersionCalls) != 0 {
		t.Errorf("InsertDeviceConfigNextVersion was called %d times, want 0 -- this was a dry run", len(repo.insertDeviceConfigNextVersionCalls))
	}
}

// TestPushDeviceConfig_MultiBoard_NoReason_Refused is FR48.2's core
// assertion: a multi-board push (more than one board in device_ids) with
// no stated reason is refused before any board is resolved or touched.
func TestPushDeviceConfig_MultiBoard_NoReason_Refused(t *testing.T) {
	repo := &fakeRepo{}
	server, _ := newScopeTestServer(repo)

	_, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceIds: []string{"device-a", "device-b"},
		Scope:     pb.PushScope_PUSH_SCOPE_COMPLETE,
		Sensors:   []*configpb.SensorConfig{sensorAt("light", 0x23, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE)},
		Reason:    "",
	})
	if err == nil {
		t.Fatal("multi-board PushDeviceConfig with no reason returned nil error, want a refusal")
	}
	detail, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error %v carries no Failure detail", err)
	}
	if detail.Class != string(contract.FailureInvalidArgument) {
		t.Errorf("Class = %q, want %q", detail.Class, contract.FailureInvalidArgument)
	}
	if detail.Field != "reason" {
		t.Errorf("Field = %q, want %q", detail.Field, "reason")
	}
	if repo.getOrCreateBoardCalls != 0 {
		t.Errorf("GetOrCreateBoard was called %d times, want 0 -- a missing reason must refuse before any board is touched", repo.getOrCreateBoardCalls)
	}
}

// TestPushDeviceConfig_MultiBoard_NoReason_WhitespaceOnly_Refused proves a
// whitespace-only reason is treated as empty (strings.TrimSpace), not
// accepted as "stated".
func TestPushDeviceConfig_MultiBoard_NoReason_WhitespaceOnly_Refused(t *testing.T) {
	repo := &fakeRepo{}
	server, _ := newScopeTestServer(repo)

	_, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceIds: []string{"device-a", "device-b"},
		Scope:     pb.PushScope_PUSH_SCOPE_COMPLETE,
		Sensors:   []*configpb.SensorConfig{sensorAt("light", 0x23, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE)},
		Reason:    "   ",
	})
	if err == nil {
		t.Fatal("multi-board PushDeviceConfig with a whitespace-only reason returned nil error, want a refusal")
	}
	detail, ok := contract.FromError(err)
	if !ok || detail.Field != "reason" {
		t.Fatalf("error %v, want an invalid_argument naming the reason field", err)
	}
}

// TestPushDeviceConfig_MultiBoard_ReasonTimestampActorAudited proves a
// multi-board push's reason, push timestamp and acting principal are
// returned on the response and recorded onto every board's own audit
// entry (FR8.2/FR48.2) -- even a board whose push otherwise fails one
// step short of Publish (the injected storage error, this package's usual
// "reached storage" proof; see assertReachedStorage's doc comment on why a
// genuinely successful non-dry-run push can't be driven through this
// fakeRepo harness).
func TestPushDeviceConfig_MultiBoard_ReasonTimestampActorAudited(t *testing.T) {
	boardARef := authz.EntityRef{Kind: authz.EntityBoard, ID: multiBoardIDA}
	boardBRef := authz.EntityRef{Kind: authz.EntityBoard, ID: multiBoardIDB}

	repo := &fakeRepo{
		getOrCreateBoardIDs:              map[string]int64{"device-a": multiBoardIDA, "device-b": multiBoardIDB},
		insertDeviceConfigNextVersionErr: errInjectedStorageFailure,
	}
	authzSvc := &fakeAuthz{
		resolveResponses: map[authz.EntityRef]authz.Resolution{
			boardARef: {Unclaimed: true},
			boardBRef: {Unclaimed: true},
		},
	}
	server := newPushValidationServer(repo, authzSvc)

	const reason = "scheduled poll_interval_ms rollout"
	resp, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceIds: []string{"device-a", "device-b"},
		Scope:     pb.PushScope_PUSH_SCOPE_COMPLETE,
		Sensors:   []*configpb.SensorConfig{sensorAt("light", 0x23, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE)},
		Reason:    reason,
	})
	if err != nil {
		t.Fatalf("multi-board PushDeviceConfig returned an RPC-level error, want per-board failures instead: %v", err)
	}

	if resp.Reason != reason {
		t.Errorf("PushDeviceConfigResponse.Reason = %q, want %q", resp.Reason, reason)
	}
	if resp.PushedAt == nil {
		t.Error("PushDeviceConfigResponse.PushedAt is nil, want the push timestamp")
	}
	if resp.ActorSubject != "alice" {
		t.Errorf("PushDeviceConfigResponse.ActorSubject = %q, want %q", resp.ActorSubject, "alice")
	}

	if len(repo.insertDeviceConfigNextVersionCalls) != 2 {
		t.Fatalf("InsertDeviceConfigNextVersion was called %d times, want 2 (one per board reaching storage)", len(repo.insertDeviceConfigNextVersionCalls))
	}
	for i, call := range repo.insertDeviceConfigNextVersionCalls {
		if call.entry.Action != audit.ActionMultiBoardPush {
			t.Errorf("call[%d].entry.Action = %q, want %q", i, call.entry.Action, audit.ActionMultiBoardPush)
		}
		if call.entry.Reason == nil || *call.entry.Reason != reason {
			t.Errorf("call[%d].entry.Reason = %v, want %q", i, call.entry.Reason, reason)
		}
		if call.entry.ActorSubject != "alice" {
			t.Errorf("call[%d].entry.ActorSubject = %q, want %q", i, call.entry.ActorSubject, "alice")
		}
	}

	// The push_group is created lazily, at most once per call -- shared by
	// every board's write, never one per board.
	if len(repo.createPushGroupCalls) != 1 {
		t.Errorf("CreatePushGroup was called %d times, want exactly 1 (shared across both boards' writes)", len(repo.createPushGroupCalls))
	}
}

// TestPushDeviceConfig_BoardSetSpanningHouseholdsCallerCannotReach_Refused
// is FR48/FR5/FR1.2's board-set guard: a board set spanning two claimed
// households is refused when the caller's Scope does not reach both --
// before either board's own push validation or write ever runs.
func TestPushDeviceConfig_BoardSetSpanningHouseholdsCallerCannotReach_Refused(t *testing.T) {
	boardARef := authz.EntityRef{Kind: authz.EntityBoard, ID: multiBoardIDA}
	boardBRef := authz.EntityRef{Kind: authz.EntityBoard, ID: multiBoardIDB}

	repo := &fakeRepo{
		getOrCreateBoardIDs: map[string]int64{"device-a": multiBoardIDA, "device-b": multiBoardIDB},
	}
	authzSvc := &fakeAuthz{
		scope: authz.NewHouseholdScope(1), // reaches household 1 only
		resolveResponses: map[authz.EntityRef]authz.Resolution{
			boardARef: {HouseholdID: 1},
			boardBRef: {HouseholdID: 2},
		},
	}
	server := newPushValidationServer(repo, authzSvc)

	_, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceIds: []string{"device-a", "device-b"},
		Scope:     pb.PushScope_PUSH_SCOPE_COMPLETE,
		Sensors:   []*configpb.SensorConfig{sensorAt("light", 0x23, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE)},
		Reason:    "coordinated firmware bump",
	})
	if err == nil {
		t.Fatal("PushDeviceConfig spanning households the caller cannot reach returned nil error, want a refusal")
	}
	detail, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error %v carries no Failure detail", err)
	}
	if detail.Class != string(contract.FailurePermissionDenied) {
		t.Errorf("Class = %q, want %q", detail.Class, contract.FailurePermissionDenied)
	}
	if detail.Field != "device_ids" {
		t.Errorf("Field = %q, want %q", detail.Field, "device_ids")
	}

	if len(repo.insertDeviceConfigNextVersionCalls) != 0 {
		t.Errorf("InsertDeviceConfigNextVersion was called %d times, want 0 -- a household-spanning refusal must write nothing for either board", len(repo.insertDeviceConfigNextVersionCalls))
	}
	if len(repo.createPushGroupCalls) != 0 {
		t.Errorf("CreatePushGroup was called %d times, want 0", len(repo.createPushGroupCalls))
	}
}

// TestPushDeviceConfig_BoardSetSpanningHouseholdsCallerReaches_NotRefused
// is the companion "not over-refused" case: a board set spanning two
// claimed households the caller's Scope reaches both of is never refused
// on that basis alone.
func TestPushDeviceConfig_BoardSetSpanningHouseholdsCallerReaches_NotRefused(t *testing.T) {
	boardARef := authz.EntityRef{Kind: authz.EntityBoard, ID: multiBoardIDA}
	boardBRef := authz.EntityRef{Kind: authz.EntityBoard, ID: multiBoardIDB}

	repo := &fakeRepo{
		getOrCreateBoardIDs: map[string]int64{"device-a": multiBoardIDA, "device-b": multiBoardIDB},
	}
	authzSvc := &fakeAuthz{
		scope: authz.NewUnionScope(authz.NewHouseholdScope(1), authz.NewHouseholdScope(2)),
		resolveResponses: map[authz.EntityRef]authz.Resolution{
			boardARef: {HouseholdID: 1},
			boardBRef: {HouseholdID: 2},
		},
	}
	server := newPushValidationServer(repo, authzSvc)

	resp, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceIds: []string{"device-a", "device-b"},
		Scope:     pb.PushScope_PUSH_SCOPE_COMPLETE,
		Sensors:   []*configpb.SensorConfig{sensorAt("light", 0x23, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE)},
		DryRun:    true,
		Reason:    "coordinated firmware bump",
	})
	if err != nil {
		t.Fatalf("PushDeviceConfig spanning households the caller reaches returned an RPC-level error, want per-board results: %v", err)
	}
	if len(resp.Results) != 2 || !resp.Results[0].Success || !resp.Results[1].Success {
		t.Errorf("Results = %+v, want both boards to succeed", resp.Results)
	}
}

// -- FR48.1: GetPushGroupStatus --------------------------------------------

// TestGetPushGroupStatus_ReportsAckedRejectedSilentAsGroup proves
// GetPushGroupStatus classifies every board in a push_group by its current
// ack state -- acked, rejected or silent -- in one response, and echoes
// back the group's own reason/timestamp/actor bookkeeping.
func TestGetPushGroupStatus_ReportsAckedRejectedSilentAsGroup(t *testing.T) {
	ackedAt := time.Now()
	repo := &fakeRepo{
		getPushGroupFound: true,
		getPushGroupResponse: PushGroupRow{
			Reason:       "coordinated rollout",
			ActorSubject: "alice",
			PushedAt:     ackedAt,
		},
		getPushGroupBoardsResponse: []PushGroupBoardRow{
			{DeviceID: "device-acked", Accepted: true, AckedAt: &ackedAt},
			{DeviceID: "device-rejected", Accepted: false, AckedAt: &ackedAt},
			{DeviceID: "device-silent", Accepted: false, AckedAt: nil},
		},
	}
	server := newPushValidationServer(repo, &fakeAuthz{})

	resp, err := server.GetPushGroupStatus(authedTestCtx("alice"), &pb.GetPushGroupStatusRequest{PushGroupId: 42})
	if err != nil {
		t.Fatalf("GetPushGroupStatus returned an error: %v", err)
	}
	if resp.Reason != "coordinated rollout" {
		t.Errorf("Reason = %q, want %q", resp.Reason, "coordinated rollout")
	}
	if resp.ActorSubject != "alice" {
		t.Errorf("ActorSubject = %q, want %q", resp.ActorSubject, "alice")
	}
	if len(resp.Boards) != 3 {
		t.Fatalf("Boards = %+v, want 3 (one per board this push_group targeted)", resp.Boards)
	}

	want := map[string]pb.AckState{
		"device-acked":    pb.AckState_ACK_STATE_ACKED,
		"device-rejected": pb.AckState_ACK_STATE_REJECTED,
		"device-silent":   pb.AckState_ACK_STATE_SILENT,
	}
	for _, b := range resp.Boards {
		wantState, ok := want[b.DeviceId]
		if !ok {
			t.Errorf("unexpected board %q in response", b.DeviceId)
			continue
		}
		if b.State != wantState {
			t.Errorf("board %q State = %v, want %v", b.DeviceId, b.State, wantState)
		}
	}
}

// TestGetPushGroupStatus_UnknownGroup_NotFound proves an unknown
// push_group_id is refused, not reported as an empty group.
func TestGetPushGroupStatus_UnknownGroup_NotFound(t *testing.T) {
	repo := &fakeRepo{getPushGroupFound: false}
	server := newPushValidationServer(repo, &fakeAuthz{})

	_, err := server.GetPushGroupStatus(authedTestCtx("alice"), &pb.GetPushGroupStatusRequest{PushGroupId: 999})
	if err == nil {
		t.Fatal("GetPushGroupStatus for an unknown push_group_id returned nil error, want a refusal")
	}
	detail, ok := contract.FromError(err)
	if !ok || detail.Class != string(contract.FailureNotFound) {
		t.Errorf("error %v, want a FailureNotFound", err)
	}
}
