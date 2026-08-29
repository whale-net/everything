package main

import (
	"strings"
	"testing"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// This file covers FR40 (rollback writes forward) at the Go-dispatch level
// (fakeRepo/fakeAuthz, no Docker/network), the same style as
// server_push_device_config_multiboard_test.go. FR40's own restore
// guarantee -- the new version's stored payload is byte-identical to
// to_version's -- and NFR6.2's append-only trigger/ack-column exception are
// covered against real SQL in
// //leaflab/api:rollback_device_config_integration_test and
// //leaflab/migrate:ownership_migration_integration_test (which embeds
// config_derived_from_migration_integration_test.go); this file's job is
// proving RollbackDeviceConfig's own dispatch logic -- refusal ordering,
// per-board results, dry_run's structural inability to reach the
// publisher, and source_never_accepted -- the same split
// push_device_config_scope_integration_test.go's own doc comment
// describes for PushDeviceConfig.
//
// liveRollbackWriter.write calls s.repo.InsertDeviceConfigNextVersion
// before ever touching s.publisher (server.go's own ordering), so -- like
// server_push_device_config_multiboard_test.go's
// TestPushDeviceConfig_MultiBoard_ReasonTimestampActorAudited -- a live
// (non-dry-run) rollback can still be driven one step short of Publish by
// injecting a storage error, letting this file assert what
// InsertDeviceConfigNextVersion was called with (derived_from_version, the
// audit entry) without a fake for *rmq.Publisher.

const (
	rollbackBoardID  = int64(201)
	rollbackBoardIDB = int64(202)
)

func newRollbackTestServer(repo *fakeRepo, authzSvc *fakeAuthz) *LeafLabAPIServer {
	return NewLeafLabAPIServer(repo, authzSvc, nil, nil, discardLogger(), defaultPollIntervalBounds)
}

// rollbackSourceConfig is the stored payload every scenario below rolls
// back to, protojson-encoded exactly the way GetConfigVersionRow returns
// it (see ConfigVersionRow.ConfigJSON's own doc comment on never being
// re-marshaled).
func rollbackSourceConfigJSON(t *testing.T) []byte {
	t.Helper()
	return []byte(`{"sensors":[{"name":"light","i2cAddress":35}]}`)
}

// -- request-level refusals -------------------------------------------------

func TestRollbackDeviceConfig_NoDeviceIds_Refused(t *testing.T) {
	repo := &fakeRepo{}
	server := newRollbackTestServer(repo, &fakeAuthz{})

	_, err := server.RollbackDeviceConfig(authedTestCtx("alice"), &pb.RollbackDeviceConfigRequest{
		ToVersion: 47,
		Reason:    "restore known-good config",
	})
	if err == nil {
		t.Fatal("RollbackDeviceConfig with no device_ids returned nil error, want a refusal")
	}
	detail, ok := contract.FromError(err)
	if !ok || detail.Field != "device_ids" {
		t.Fatalf("error %v, want an invalid_argument naming device_ids", err)
	}
}

func TestRollbackDeviceConfig_NoReason_Refused_WritesNothing(t *testing.T) {
	repo := &fakeRepo{}
	server := newRollbackTestServer(repo, &fakeAuthz{})

	_, err := server.RollbackDeviceConfig(authedTestCtx("alice"), &pb.RollbackDeviceConfigRequest{
		DeviceIds: []string{"device-a"},
		ToVersion: 47,
		Reason:    "",
	})
	if err == nil {
		t.Fatal("RollbackDeviceConfig with no reason returned nil error, want a refusal")
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
	// Unlike PushDeviceConfigRequest.reason (required only past one
	// board), FR40's own doc comment says a rollback always states why --
	// even a single-board request must be refused before any board is
	// touched.
	if repo.getOrCreateBoardCalls != 0 {
		t.Errorf("GetOrCreateBoard was called %d times, want 0 -- a missing reason must refuse before any board is touched", repo.getOrCreateBoardCalls)
	}
}

func TestRollbackDeviceConfig_WhitespaceReason_Refused(t *testing.T) {
	repo := &fakeRepo{}
	server := newRollbackTestServer(repo, &fakeAuthz{})

	_, err := server.RollbackDeviceConfig(authedTestCtx("alice"), &pb.RollbackDeviceConfigRequest{
		DeviceIds: []string{"device-a"},
		ToVersion: 47,
		Reason:    "   ",
	})
	if err == nil {
		t.Fatal("RollbackDeviceConfig with a whitespace-only reason returned nil error, want a refusal")
	}
	detail, ok := contract.FromError(err)
	if !ok || detail.Field != "reason" {
		t.Fatalf("error %v, want an invalid_argument naming the reason field", err)
	}
}

// -- per-board outcomes -------------------------------------------------

func TestRollbackDeviceConfig_ToVersionNotFound_PerBoardFailureNotRPCError(t *testing.T) {
	boardRef := authz.EntityRef{Kind: authz.EntityBoard, ID: rollbackBoardID}
	repo := &fakeRepo{getOrCreateBoardID: rollbackBoardID} // getConfigVersionRowResponses left empty: version 47 is unknown
	authzSvc := &fakeAuthz{resolveResponses: map[authz.EntityRef]authz.Resolution{boardRef: {Unclaimed: true}}}
	server := newRollbackTestServer(repo, authzSvc)

	resp, err := server.RollbackDeviceConfig(authedTestCtx("alice"), &pb.RollbackDeviceConfigRequest{
		DeviceIds: []string{"device-a"},
		ToVersion: 47,
		Reason:    "restore known-good config",
	})
	if err != nil {
		t.Fatalf("RollbackDeviceConfig returned an RPC-level error, want a per-board failure instead: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("Results = %+v, want exactly 1", resp.Results)
	}
	result := resp.Results[0]
	if result.Success {
		t.Fatalf("Success = true, want false (to_version 47 does not exist)")
	}
	if result.Failure == nil || result.Failure.Class != string(contract.FailureNotFound) {
		t.Errorf("Failure = %+v, want a FailureNotFound", result.Failure)
	}
}

func TestRollbackDeviceConfig_DryRun_WritesNothing_EffectiveConfigIsSourcePayload(t *testing.T) {
	boardRef := authz.EntityRef{Kind: authz.EntityBoard, ID: rollbackBoardID}
	repo := &fakeRepo{
		getOrCreateBoardID: rollbackBoardID,
		getConfigVersionRowResponses: map[uint64]ConfigVersionRow{
			47: {ConfigJSON: rollbackSourceConfigJSON(t), Accepted: true},
		},
		// A different, unrelated current config -- proves the rollback's
		// effective_config is to_version's own sensors, not something
		// merged with whatever is currently live (FR40's restore
		// guarantee, dispatch-level half).
		getLatestAcceptedConfigResponse: &configpb.DeviceConfig{
			Sensors: []*configpb.SensorConfig{{Name: "unrelated-current-sensor", I2CAddress: 0x11}},
		},
	}
	authzSvc := &fakeAuthz{resolveResponses: map[authz.EntityRef]authz.Resolution{boardRef: {Unclaimed: true}}}
	server := newRollbackTestServer(repo, authzSvc)

	resp, err := server.RollbackDeviceConfig(authedTestCtx("alice"), &pb.RollbackDeviceConfigRequest{
		DeviceIds: []string{"device-a"},
		ToVersion: 47,
		Reason:    "restore known-good config",
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("dry-run RollbackDeviceConfig returned an error, want success: %v", err)
	}
	if len(resp.Results) != 1 || !resp.Results[0].Success {
		t.Fatalf("Results = %+v, want one successful result", resp.Results)
	}
	result := resp.Results[0]

	sensors := result.EffectiveConfig.GetSensors()
	if len(sensors) != 1 || sensors[0].Name != "light" {
		t.Errorf("EffectiveConfig.Sensors = %+v, want exactly to_version 47's own 'light' sensor", sensors)
	}
	if result.DerivedFromVersion != 47 {
		t.Errorf("DerivedFromVersion = %d, want 47", result.DerivedFromVersion)
	}
	if len(repo.insertDeviceConfigNextVersionCalls) != 0 {
		t.Errorf("InsertDeviceConfigNextVersion was called %d times, want 0 -- a dry run must write nothing", len(repo.insertDeviceConfigNextVersionCalls))
	}
	// The prior current config's sensor is absent from to_version 47's
	// payload -- FR40's dry-run preview must show it as a removal, exactly
	// as FR38's own dry-run preview does for an ordinary push.
	found := false
	for _, r := range result.Removed {
		if r.I2CAddress == 0x11 {
			found = true
		}
	}
	if !found {
		t.Errorf("Removed = %+v, want the unrelated-current-sensor's entry listed as a removal", result.Removed)
	}
}

func TestRollbackDeviceConfig_DryRun_MultiBoard_PerBoardResults(t *testing.T) {
	boardARef := authz.EntityRef{Kind: authz.EntityBoard, ID: rollbackBoardID}
	boardBRef := authz.EntityRef{Kind: authz.EntityBoard, ID: rollbackBoardIDB}
	repo := &fakeRepo{
		getOrCreateBoardIDs: map[string]int64{"device-a": rollbackBoardID, "device-b": rollbackBoardIDB},
		getConfigVersionRowResponses: map[uint64]ConfigVersionRow{
			47: {ConfigJSON: rollbackSourceConfigJSON(t), Accepted: true},
		},
	}
	authzSvc := &fakeAuthz{
		resolveResponses: map[authz.EntityRef]authz.Resolution{
			boardARef: {Unclaimed: true},
			boardBRef: {Unclaimed: true},
		},
	}
	server := newRollbackTestServer(repo, authzSvc)

	resp, err := server.RollbackDeviceConfig(authedTestCtx("alice"), &pb.RollbackDeviceConfigRequest{
		DeviceIds: []string{"device-a", "device-b"},
		ToVersion: 47,
		Reason:    "coordinated rollback",
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("dry-run multi-board RollbackDeviceConfig returned an error, want success: %v", err)
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
	if resp.Reason != "coordinated rollback" {
		t.Errorf("Reason = %q, want %q", resp.Reason, "coordinated rollback")
	}
	if resp.ActorSubject != "alice" {
		t.Errorf("ActorSubject = %q, want %q", resp.ActorSubject, "alice")
	}
}

func TestRollbackDeviceConfig_BoardSetSpanningHouseholdsCallerCannotReach_Refused(t *testing.T) {
	boardARef := authz.EntityRef{Kind: authz.EntityBoard, ID: rollbackBoardID}
	boardBRef := authz.EntityRef{Kind: authz.EntityBoard, ID: rollbackBoardIDB}
	repo := &fakeRepo{
		getOrCreateBoardIDs: map[string]int64{"device-a": rollbackBoardID, "device-b": rollbackBoardIDB},
	}
	authzSvc := &fakeAuthz{
		scope: authz.NewHouseholdScope(1), // reaches household 1 only
		resolveResponses: map[authz.EntityRef]authz.Resolution{
			boardARef: {HouseholdID: 1},
			boardBRef: {HouseholdID: 2},
		},
	}
	server := newRollbackTestServer(repo, authzSvc)

	_, err := server.RollbackDeviceConfig(authedTestCtx("alice"), &pb.RollbackDeviceConfigRequest{
		DeviceIds: []string{"device-a", "device-b"},
		ToVersion: 47,
		Reason:    "coordinated rollback",
	})
	if err == nil {
		t.Fatal("RollbackDeviceConfig spanning households the caller cannot reach returned nil error, want a refusal")
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
}

// -- FR35.2/source_never_accepted -------------------------------------------

func TestRollbackDeviceConfig_RejectedSourceVersion_SourceNeverAcceptedTrue(t *testing.T) {
	boardRef := authz.EntityRef{Kind: authz.EntityBoard, ID: rollbackBoardID}
	repo := &fakeRepo{
		getOrCreateBoardID: rollbackBoardID,
		getConfigVersionRowResponses: map[uint64]ConfigVersionRow{
			47: {ConfigJSON: rollbackSourceConfigJSON(t), Accepted: false}, // rejected or still-pending
		},
	}
	authzSvc := &fakeAuthz{resolveResponses: map[authz.EntityRef]authz.Resolution{boardRef: {Unclaimed: true}}}
	server := newRollbackTestServer(repo, authzSvc)

	resp, err := server.RollbackDeviceConfig(authedTestCtx("alice"), &pb.RollbackDeviceConfigRequest{
		DeviceIds: []string{"device-a"},
		ToVersion: 47,
		Reason:    "restore despite rejection",
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("RollbackDeviceConfig to a rejected/pending version returned an error, want it permitted (FR35.2): %v", err)
	}
	result := resp.Results[0]
	if !result.Success {
		t.Fatalf("Success = false, want true -- FR35.2 makes any version fetchable, rolling back to it is permitted: %+v", result.Failure)
	}
	if !result.SourceNeverAccepted {
		t.Error("SourceNeverAccepted = false, want true -- to_version 47 was never accepted, the caller must be told so")
	}
}

func TestRollbackDeviceConfig_AcceptedSourceVersion_SourceNeverAcceptedFalse(t *testing.T) {
	boardRef := authz.EntityRef{Kind: authz.EntityBoard, ID: rollbackBoardID}
	repo := &fakeRepo{
		getOrCreateBoardID: rollbackBoardID,
		getConfigVersionRowResponses: map[uint64]ConfigVersionRow{
			47: {ConfigJSON: rollbackSourceConfigJSON(t), Accepted: true},
		},
	}
	authzSvc := &fakeAuthz{resolveResponses: map[authz.EntityRef]authz.Resolution{boardRef: {Unclaimed: true}}}
	server := newRollbackTestServer(repo, authzSvc)

	resp, err := server.RollbackDeviceConfig(authedTestCtx("alice"), &pb.RollbackDeviceConfigRequest{
		DeviceIds: []string{"device-a"},
		ToVersion: 47,
		Reason:    "restore known-good config",
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("RollbackDeviceConfig returned an error, want success: %v", err)
	}
	if resp.Results[0].SourceNeverAccepted {
		t.Error("SourceNeverAccepted = true, want false -- to_version 47 was accepted")
	}
}

// -- live write path (one step short of Publish) -----------------------

// TestRollbackDeviceConfig_ReachesStorage_DerivedFromVersionAndAuditEntry
// proves the live (non-dry-run) path's persistence step: the
// derived_from_version passed to InsertDeviceConfigNextVersion is
// to_version, and the audit entry it carries is FR8/FR40's own -- action
// Rollback, and a reason that names both the source version and the
// caller's stated reason (audit.NewRollbackEntry's own doc comment on
// where toVersion is recorded, since Entry has only one free-text Reason
// slot).
func TestRollbackDeviceConfig_ReachesStorage_DerivedFromVersionAndAuditEntry(t *testing.T) {
	boardRef := authz.EntityRef{Kind: authz.EntityBoard, ID: rollbackBoardID}
	repo := &fakeRepo{
		getOrCreateBoardID: rollbackBoardID,
		getConfigVersionRowResponses: map[uint64]ConfigVersionRow{
			47: {ConfigJSON: rollbackSourceConfigJSON(t), Accepted: true},
		},
		insertDeviceConfigNextVersionErr: errInjectedStorageFailure,
	}
	authzSvc := &fakeAuthz{resolveResponses: map[authz.EntityRef]authz.Resolution{boardRef: {HouseholdID: 9}}}
	server := newRollbackTestServer(repo, authzSvc)

	const reason = "revert accidental sensor removal"
	resp, err := server.RollbackDeviceConfig(authedTestCtx("alice"), &pb.RollbackDeviceConfigRequest{
		DeviceIds: []string{"device-a"},
		ToVersion: 47,
		Reason:    reason,
	})
	if err != nil {
		t.Fatalf("RollbackDeviceConfig returned an RPC-level error, want a per-board failure instead: %v", err)
	}
	if resp.Results[0].Success {
		t.Fatalf("Results[0].Success = true, want false (storage was injected to fail)")
	}

	if len(repo.insertDeviceConfigNextVersionCalls) != 1 {
		t.Fatalf("InsertDeviceConfigNextVersion was called %d times, want 1 (the live path reached storage)", len(repo.insertDeviceConfigNextVersionCalls))
	}
	call := repo.insertDeviceConfigNextVersionCalls[0]

	if call.derivedFromVersion == nil || *call.derivedFromVersion != 47 {
		t.Errorf("derivedFromVersion = %v, want a pointer to 47", call.derivedFromVersion)
	}
	// FR40's restore guarantee rests on the stored payload being passed
	// through verbatim -- never re-marshaled from the decoded cfgProto
	// (ConfigVersionRow.ConfigJSON's own doc comment) -- so the exact
	// bytes GetConfigVersionRow returned for to_version 47 must be what
	// reaches storage, byte for byte.
	if string(call.configJSON) != string(rollbackSourceConfigJSON(t)) {
		t.Errorf("configJSON passed to InsertDeviceConfigNextVersion = %s, want to_version 47's own stored payload verbatim %s", call.configJSON, rollbackSourceConfigJSON(t))
	}
	if call.entry.Action != audit.ActionRollback {
		t.Errorf("entry.Action = %q, want %q", call.entry.Action, audit.ActionRollback)
	}
	if call.entry.EntityKind != "device_config" {
		t.Errorf("entry.EntityKind = %q, want %q", call.entry.EntityKind, "device_config")
	}
	if call.entry.ActorSubject != "alice" {
		t.Errorf("entry.ActorSubject = %q, want %q", call.entry.ActorSubject, "alice")
	}
	if call.entry.Reason == nil {
		t.Fatal("entry.Reason is nil, want a reason recording both the source version and the caller's stated reason")
	}
	if !strings.Contains(*call.entry.Reason, "47") {
		t.Errorf("entry.Reason = %q, want it to name the source version 47", *call.entry.Reason)
	}
	if !strings.Contains(*call.entry.Reason, reason) {
		t.Errorf("entry.Reason = %q, want it to contain the caller's stated reason %q", *call.entry.Reason, reason)
	}
	if call.entry.TargetHouseholdID == nil || *call.entry.TargetHouseholdID != 9 {
		t.Errorf("entry.TargetHouseholdID = %v, want a pointer to 9 (the board's own household)", call.entry.TargetHouseholdID)
	}
}
