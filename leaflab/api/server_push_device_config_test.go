package main

import (
	"testing"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// This file covers FR1.2/FR1.3's push-time live-reference invariant on
// PushDeviceConfig, at the Go-dispatch level with fakeRepo/fakeAuthz --
// this is the task's named main-today defect reproduction ("an
// authenticated owner can point their own sensor at another household's
// region"): before this task, ApplyConfigRegions (leaflab/processor) wrote
// sensor.region_id with no ownership check at all, and PushDeviceConfig
// itself performed no check either.
//
// These tests never reach s.publisher (a concrete *rmq.Publisher this repo
// has no in-process fake for -- see libs/go/rmq/publisher_test.go, which
// skips its own "requires RabbitMQ" cases): a refused push returns before
// touching it, and the "passes validation" test injects a storage error at
// InsertDeviceConfigNextVersion specifically to stop execution one step
// before Publish, while still proving FR1.2/FR1.3's validation gate let a
// same-household push through to the storage step.

const foreignRegionID = 42
const sameHouseholdRegionID = 43

func newPushValidationServer(repo *fakeRepo, authzSvc *fakeAuthz) *LeafLabAPIServer {
	return NewLeafLabAPIServer(repo, authzSvc, nil, nil, nil, discardLogger())
}

// TestPushDeviceConfig_ForeignHouseholdRegion_Refused_WritesNothing is this
// task's core defect reproduction: a board in household 1 pushes a config
// naming a region_id that resolves to household 2. FR1.3 requires the
// whole push refused as invalid_argument, naming the offending entry and
// field, with nothing stored -- proven here by asserting
// InsertDeviceConfigNextVersion was never called (fakeRepo has no
// publisher to reach either way, but see the file doc comment above for
// why this is still a decisive assertion).
func TestPushDeviceConfig_ForeignHouseholdRegion_Refused_WritesNothing(t *testing.T) {
	const boardID = 99
	boardRef := authz.EntityRef{Kind: authz.EntityBoard, ID: boardID}
	regionRef := authz.EntityRef{Kind: authz.EntityRegion, ID: foreignRegionID}

	repo := &fakeRepo{getOrCreateBoardID: boardID}
	authzSvc := &fakeAuthz{
		resolveResponses: map[authz.EntityRef]authz.Resolution{
			boardRef:  {HouseholdID: 1},
			regionRef: {HouseholdID: 2}, // a different household than the board's own
		},
	}
	server := newPushValidationServer(repo, authzSvc)

	_, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Scope:    pb.PushScope_PUSH_SCOPE_COMPLETE,
		Sensors: []*configpb.SensorConfig{
			{RegionId: foreignRegionID},
		},
	})
	if err == nil {
		t.Fatal("PushDeviceConfig referencing a foreign household's region_id returned nil error, want a refusal")
	}

	detail, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error %v carries no Failure detail", err)
	}
	if detail.Class != string(contract.FailureInvalidArgument) {
		t.Errorf("Class = %q, want %q", detail.Class, contract.FailureInvalidArgument)
	}
	if detail.Entity != string(authz.EntityRegion) {
		t.Errorf("Entity = %q, want %q (naming the offending entry)", detail.Entity, authz.EntityRegion)
	}
	if detail.Field != "sensors[0].region_id" {
		t.Errorf("Field = %q, want %q (naming the offending field)", detail.Field, "sensors[0].region_id")
	}

	if repo.getOrCreateBoardCalls != 1 {
		t.Errorf("GetOrCreateBoard calls = %d, want 1 -- self-registration is not a config write and must still run", repo.getOrCreateBoardCalls)
	}
	if len(repo.insertDeviceConfigNextVersionCalls) != 0 {
		t.Errorf("InsertDeviceConfigNextVersion was called %d times, want 0 -- a refused push must write nothing", len(repo.insertDeviceConfigNextVersionCalls))
	}
}

// TestPushDeviceConfig_NoPartialApplication_OneBadEntryRefusesWholeBatch is
// FR1.3's "no partial application": a payload with one valid entry and one
// foreign-household entry is refused in full -- zero device_config rows
// written, not one row written with the bad entry silently dropped.
func TestPushDeviceConfig_NoPartialApplication_OneBadEntryRefusesWholeBatch(t *testing.T) {
	const boardID = 99
	boardRef := authz.EntityRef{Kind: authz.EntityBoard, ID: boardID}
	goodRegionRef := authz.EntityRef{Kind: authz.EntityRegion, ID: sameHouseholdRegionID}
	badRegionRef := authz.EntityRef{Kind: authz.EntityRegion, ID: foreignRegionID}

	repo := &fakeRepo{getOrCreateBoardID: boardID}
	authzSvc := &fakeAuthz{
		resolveResponses: map[authz.EntityRef]authz.Resolution{
			boardRef:      {HouseholdID: 1},
			goodRegionRef: {HouseholdID: 1},
			badRegionRef:  {HouseholdID: 2},
		},
	}
	server := newPushValidationServer(repo, authzSvc)

	_, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Scope:    pb.PushScope_PUSH_SCOPE_COMPLETE,
		Sensors: []*configpb.SensorConfig{
			{Name: "good-sensor", RegionId: sameHouseholdRegionID},
			{Name: "bad-sensor", RegionId: foreignRegionID},
		},
	})
	if err == nil {
		t.Fatal("PushDeviceConfig with one bad entry returned nil error, want a refusal")
	}
	detail, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error %v carries no Failure detail", err)
	}
	if detail.Field != "sensors[1].region_id" {
		t.Errorf("Field = %q, want %q (the bad entry's own field, not the good one's)", detail.Field, "sensors[1].region_id")
	}
	if len(repo.insertDeviceConfigNextVersionCalls) != 0 {
		t.Errorf("InsertDeviceConfigNextVersion was called %d times, want 0 -- one bad entry must refuse the entire batch, not just itself", len(repo.insertDeviceConfigNextVersionCalls))
	}
}

// TestPushDeviceConfig_SameHouseholdRegion_PassesValidation_ReachesStorage
// is the companion "not over-refused" case: a region_id that resolves to
// the pushing board's own household must not be rejected by
// FR1.2/FR1.3's validation gate. fakeRepo.insertDeviceConfigNextVersionErr
// is injected specifically so the call stops one step short of
// s.publisher.Publish (a concrete *rmq.Publisher this test has no fake
// for -- see file doc comment) while still proving validation let this
// push through: the returned error is FailureInternal (the injected
// storage failure), not FailureInvalidArgument, and
// InsertDeviceConfigNextVersion was actually called.
func TestPushDeviceConfig_SameHouseholdRegion_PassesValidation_ReachesStorage(t *testing.T) {
	const boardID = 99
	boardRef := authz.EntityRef{Kind: authz.EntityBoard, ID: boardID}
	regionRef := authz.EntityRef{Kind: authz.EntityRegion, ID: sameHouseholdRegionID}

	repo := &fakeRepo{
		getOrCreateBoardID:               boardID,
		insertDeviceConfigNextVersionErr: errInjectedStorageFailure,
	}
	authzSvc := &fakeAuthz{
		resolveResponses: map[authz.EntityRef]authz.Resolution{
			boardRef:  {HouseholdID: 1},
			regionRef: {HouseholdID: 1},
		},
	}
	server := newPushValidationServer(repo, authzSvc)

	_, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Scope:    pb.PushScope_PUSH_SCOPE_COMPLETE,
		Sensors: []*configpb.SensorConfig{
			{Name: "sensor-1", RegionId: sameHouseholdRegionID},
		},
	})
	if err == nil {
		t.Fatal("PushDeviceConfig returned nil error, want the injected storage failure (test setup)")
	}
	detail, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error %v carries no Failure detail", err)
	}
	if detail.Class == string(contract.FailureInvalidArgument) {
		t.Errorf("Class = %q -- a same-household region_id was rejected by the write-invariant check, want it to pass validation", detail.Class)
	}
	if detail.Class != string(contract.FailureInternal) {
		t.Errorf("Class = %q, want %q (the injected storage failure, proving this ran past validation)", detail.Class, contract.FailureInternal)
	}

	if len(repo.insertDeviceConfigNextVersionCalls) != 1 {
		t.Fatalf("InsertDeviceConfigNextVersion was called %d times, want 1 -- a same-household push must reach the storage step", len(repo.insertDeviceConfigNextVersionCalls))
	}
	if got := repo.insertDeviceConfigNextVersionCalls[0].boardID; got != boardID {
		t.Errorf("InsertDeviceConfigNextVersion boardID = %d, want %d", got, boardID)
	}
}

var errInjectedStorageFailure = &testStorageError{}

// testStorageError is a distinct error type (not errors.New's *errorString)
// so it's unambiguous in test failure output that this is the
// deliberately-injected failure and not some other error class.
type testStorageError struct{}

func (*testStorageError) Error() string { return "injected storage failure (test setup)" }
