package main

import (
	"testing"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"google.golang.org/protobuf/proto"
)

// This file covers FR37's DiffConfigVersions RPC (server.go) at the
// Go-dispatch level with fakeRepo/fakeAuthz -- config/diff_test.go already
// covers config.Diff's own classification logic in isolation; this file's
// job is proving the RPC's wiring: resolving stored versions and unpushed
// drafts on either side (through the same FR82 scope semantics
// PushDeviceConfig itself uses), and returning both raw payloads alongside
// the classification.

const diffTestBoardID = int64(88)

var diffTestBoardRef = authz.EntityRef{Kind: authz.EntityBoard, ID: diffTestBoardID}

func newDiffTestServer(repo *fakeRepo) *LeafLabAPIServer {
	authzSvc := &fakeAuthz{
		scope:      allPermittingScope{},
		resolveRef: diffTestBoardRef,
		resolveRes: authz.Resolution{HouseholdID: 1},
	}
	return NewLeafLabAPIServer(repo, authzSvc, nil, nil, discardLogger(), defaultPollIntervalBounds)
}

func versionSide(v uint64) *pb.ConfigSide {
	return &pb.ConfigSide{Side: &pb.ConfigSide_Version{Version: v}}
}

func draftSide(draft *pb.ConfigDraft) *pb.ConfigSide {
	return &pb.ConfigSide{Side: &pb.ConfigSide_Draft{Draft: draft}}
}

// TestDiffConfigVersions_TwoStoredVersions_ClassifiesAndReturnsBothPayloads
// covers FR37's basic shape: diffing two stored versions classifies each
// entry (added/removed/unchanged here) and returns both raw payloads
// byte-identical to what was stored.
func TestDiffConfigVersions_TwoStoredVersions_ClassifiesAndReturnsBothPayloads(t *testing.T) {
	v1 := &configpb.DeviceConfig{DeviceId: "device-a", Sensors: []*configpb.SensorConfig{
		sensorAt("keep", 0x10, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE),
		sensorAt("dropped", 0x20, firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE),
	}}
	v2 := &configpb.DeviceConfig{DeviceId: "device-a", Sensors: []*configpb.SensorConfig{
		sensorAt("keep", 0x10, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE),
		sensorAt("added", 0x30, firmwarepb.SensorType_SENSOR_TYPE_HUMIDITY),
	}}
	repo := &fakeRepo{getConfigVersionResponses: map[uint64]*configpb.DeviceConfig{1: v1, 2: v2}}
	server := newDiffTestServer(repo)

	resp, err := server.DiffConfigVersions(authedTestCtx("alice"), &pb.DiffConfigVersionsRequest{
		DeviceId: "device-a",
		From:     versionSide(1),
		To:       versionSide(2),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Asserted by i2c_address (EntryDiff carries no name) and by Kind
	// together, not just per-Kind counts -- a from/to argument swap
	// produces the same counts (one REMOVED, one ADDED, one UNCHANGED) but
	// classifies the wrong address as each, which a counts-only assertion
	// would miss.
	byAddr := map[uint32]pb.DiffKind{}
	for _, e := range resp.Entries {
		byAddr[e.I2CAddress] = e.Kind
	}
	if len(resp.Entries) != 3 {
		t.Fatalf("Entries = %+v, want 3", resp.Entries)
	}
	if byAddr[0x20] != pb.DiffKind_DIFF_KIND_REMOVED {
		t.Errorf("0x20 ('dropped', in v1 but not v2) classified %v, want DIFF_KIND_REMOVED", byAddr[0x20])
	}
	if byAddr[0x30] != pb.DiffKind_DIFF_KIND_ADDED {
		t.Errorf("0x30 ('added', in v2 but not v1) classified %v, want DIFF_KIND_ADDED", byAddr[0x30])
	}
	if byAddr[0x10] != pb.DiffKind_DIFF_KIND_UNCHANGED {
		t.Errorf("0x10 ('keep', identical in both) classified %v, want DIFF_KIND_UNCHANGED", byAddr[0x10])
	}

	if !proto.Equal(resp.From, v1) {
		t.Errorf("From = %v, want byte-identical to stored version 1 %v", resp.From, v1)
	}
	if !proto.Equal(resp.To, v2) {
		t.Errorf("To = %v, want byte-identical to stored version 2 %v", resp.To, v2)
	}
}

// TestDiffConfigVersions_UnknownVersion_NotFound covers the "from"/"to"
// resolution's not-found path: a version with no stored config is
// refused, naming the offending side.
func TestDiffConfigVersions_UnknownVersion_NotFound(t *testing.T) {
	repo := &fakeRepo{}
	server := newDiffTestServer(repo)

	_, err := server.DiffConfigVersions(authedTestCtx("alice"), &pb.DiffConfigVersionsRequest{
		DeviceId: "device-a",
		From:     versionSide(99),
		To:       versionSide(1),
	})
	if err == nil {
		t.Fatal("DiffConfigVersions with an unknown version returned nil error, want a refusal")
	}
	detail, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error %v carries no Failure detail", err)
	}
	if detail.Class != string(contract.FailureNotFound) {
		t.Errorf("Class = %q, want %q", detail.Class, contract.FailureNotFound)
	}
}

// TestDiffConfigVersions_MissingFromSide_InvalidArgument covers FR37/
// FR82.1's "no default, never inferred" posture: an unset `from` oneof is
// refused before either side is resolved.
func TestDiffConfigVersions_MissingFromSide_InvalidArgument(t *testing.T) {
	repo := &fakeRepo{}
	server := newDiffTestServer(repo)

	_, err := server.DiffConfigVersions(authedTestCtx("alice"), &pb.DiffConfigVersionsRequest{
		DeviceId: "device-a",
		From:     nil,
		To:       versionSide(1),
	})
	if err == nil {
		t.Fatal("DiffConfigVersions with no from side returned nil error, want a refusal")
	}
	detail, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error %v carries no Failure detail", err)
	}
	if detail.Class != string(contract.FailureInvalidArgument) {
		t.Errorf("Class = %q, want %q", detail.Class, contract.FailureInvalidArgument)
	}
	if detail.Field != "from" {
		t.Errorf("Field = %q, want %q", detail.Field, "from")
	}
}

// TestDiffConfigVersions_REMOVED_ReachableFromEditDraft covers FR37's
// stated property, exercised through the real RPC: diffing a stored
// version against an unpushed EDIT draft that removes one of its entries
// classifies that entry REMOVED -- the draft is materialised through the
// same FR82 scope semantics a real EDIT push would use (materialiseDraft),
// never stored.
func TestDiffConfigVersions_REMOVED_ReachableFromEditDraft(t *testing.T) {
	base := &configpb.DeviceConfig{DeviceId: "device-a", Sensors: []*configpb.SensorConfig{
		sensorAt("keep", 0x10, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE),
		sensorAt("drop", 0x20, firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE),
	}}
	repo := &fakeRepo{
		getConfigVersionResponses:       map[uint64]*configpb.DeviceConfig{1: base},
		getLatestAcceptedConfigResponse: base,
	}
	server := newDiffTestServer(repo)

	resp, err := server.DiffConfigVersions(authedTestCtx("alice"), &pb.DiffConfigVersionsRequest{
		DeviceId: "device-a",
		From:     versionSide(1),
		To: draftSide(&pb.ConfigDraft{
			Scope:   pb.PushScope_PUSH_SCOPE_EDIT,
			Removes: []*pb.RemoveKey{{I2CAddress: 0x20}},
		}),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var removed []*pb.EntryDiff
	for _, e := range resp.Entries {
		if e.Kind == pb.DiffKind_DIFF_KIND_REMOVED {
			removed = append(removed, e)
		}
	}
	if len(removed) != 1 || removed[0].I2CAddress != 0x20 {
		t.Fatalf("REMOVED entries = %+v, want exactly one at i2c_address 0x20", removed)
	}
	// Never stored or published -- the draft side must not touch storage.
	if len(repo.insertDeviceConfigNextVersionCalls) != 0 {
		t.Errorf("draft diff wrote %d config versions, want 0 -- a draft is never stored or published", len(repo.insertDeviceConfigNextVersionCalls))
	}
}

// TestDiffConfigVersions_REMOVED_ReachableFromCompleteDraftOmission covers
// FR37's other stated path to REMOVED: a scope=COMPLETE draft that simply
// omits an entry the stored version had -- no remove key at all -- still
// classifies REMOVED.
func TestDiffConfigVersions_REMOVED_ReachableFromCompleteDraftOmission(t *testing.T) {
	base := &configpb.DeviceConfig{DeviceId: "device-a", Sensors: []*configpb.SensorConfig{
		sensorAt("keep", 0x10, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE),
		sensorAt("omitted", 0x20, firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE),
	}}
	repo := &fakeRepo{getConfigVersionResponses: map[uint64]*configpb.DeviceConfig{1: base}}
	server := newDiffTestServer(repo)

	resp, err := server.DiffConfigVersions(authedTestCtx("alice"), &pb.DiffConfigVersionsRequest{
		DeviceId: "device-a",
		From:     versionSide(1),
		To: draftSide(&pb.ConfigDraft{
			Scope:   pb.PushScope_PUSH_SCOPE_COMPLETE,
			Sensors: []*configpb.SensorConfig{sensorAt("keep", 0x10, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE)},
		}),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var removed []*pb.EntryDiff
	for _, e := range resp.Entries {
		if e.Kind == pb.DiffKind_DIFF_KIND_REMOVED {
			removed = append(removed, e)
		}
	}
	if len(removed) != 1 || removed[0].I2CAddress != 0x20 {
		t.Fatalf("REMOVED entries = %+v, want exactly one at i2c_address 0x20", removed)
	}
	// scope=COMPLETE never materialises against a base -- proves the
	// omission alone (not a remove key) drove the REMOVED classification.
	if repo.getLatestAcceptedConfigCalls != 0 {
		t.Errorf("GetLatestAcceptedConfig was called %d times, want 0 for a scope=COMPLETE draft", repo.getLatestAcceptedConfigCalls)
	}
}

// TestDiffConfigVersions_UnpushedDraftOnBothSides covers FR37's "a diff
// ... between a version and an unpushed draft" extended to both sides at
// once: neither side is a stored version, and the diff still runs,
// touching no storage or catalog lookup on either side.
func TestDiffConfigVersions_UnpushedDraftOnBothSides(t *testing.T) {
	repo := &fakeRepo{}
	server := newDiffTestServer(repo)

	resp, err := server.DiffConfigVersions(authedTestCtx("alice"), &pb.DiffConfigVersionsRequest{
		DeviceId: "device-a",
		From: draftSide(&pb.ConfigDraft{
			Scope:   pb.PushScope_PUSH_SCOPE_COMPLETE,
			Sensors: []*configpb.SensorConfig{sensorAt("a", 0x10, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE)},
		}),
		To: draftSide(&pb.ConfigDraft{
			Scope:   pb.PushScope_PUSH_SCOPE_COMPLETE,
			Sensors: []*configpb.SensorConfig{sensorAt("b", 0x20, firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE)},
		}),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Entries) != 2 {
		t.Fatalf("Entries = %+v, want 2 (one REMOVED 'a', one ADDED 'b')", resp.Entries)
	}
	if len(repo.insertDeviceConfigNextVersionCalls) != 0 {
		t.Errorf("draft-vs-draft diff wrote %d config versions, want 0", len(repo.insertDeviceConfigNextVersionCalls))
	}
	if repo.getLatestAcceptedConfigCalls != 0 {
		t.Errorf("GetLatestAcceptedConfig was called %d times, want 0 -- neither side is scope=EDIT", repo.getLatestAcceptedConfigCalls)
	}
}
